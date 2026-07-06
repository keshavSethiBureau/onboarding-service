package auth0

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	hc "github.com/Bureau-Inc/bureau-commons-go/httpclient"
	httpconfig "github.com/Bureau-Inc/bureau-commons-go/httpclient/config"
	"github.com/Bureau-Inc/bureau-commons-go/httpclient/dtos"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

const (
	svcAuth0 = "auth0mgmt"

	apiToken      = "getToken"
	apiCreateOrg  = "createOrg"
	apiUserOrgs   = "getUserOrgs"
	apiGetUser    = "getUser"
	apiAddMembers = "addMembers"
	apiAddRoles   = "addMemberRoles"
	apiDeleteOrg  = "deleteOrg"

	// selfSignedUp mirrors the auth service, which hardcodes this on org metadata.
	selfSignedUp = "True"

	// tokenExpiryBuffer refetches the M2M token this long before it expires,
	// matching the auth service (expires_in - 1000s).
	tokenExpiryBuffer = 1000 * time.Second

	// Bureau branding applied to every self-signup org (auth service parity).
	brandPrimary = "#1C2D77"
	brandPageBg  = "#F8F9FB"
)

// ErrUserAlreadyOwnsOrg mirrors the auth service's "User already has an
// organisation" rejection (is_user_owns_org). Callers decide how to surface it
// (e.g. a Temporal activity may wrap it as a non-retryable error).
var ErrUserAlreadyOwnsOrg = errors.New("user already owns an organisation")

// Auth0Settings holds the Auth0 Management credentials and the tenant object ids
// the org-creation flow needs (connections to enable, the owner role to assign).
type Auth0Settings struct {
	Domain                       string
	ClientID                     string
	ClientSecret                 string
	Audience                     string
	UsernamePasswordConnectionID string
	SSOConnectionID              string
	OwnerRoleID                  string
}

// HTTPOrgCreator creates Auth0 organisations via the Management API over commons
// httpclient, faithfully porting the auth service's create-organisation flow:
// one org per user, a random org name, Bureau branding + metadata, both login
// connections enabled, the user added as a member and given the owner role, with
// delete-on-failure cleanup so a retry starts clean.
type HTTPOrgCreator struct {
	http *hc.BureauHttpClient
	cfg  Auth0Settings

	mu    sync.Mutex
	token string
	exp   time.Time
}

// NewHTTPOrgCreator builds the Auth0 Management org creator. Constructs fine with
// empty settings (calls fail until real credentials are supplied).
func NewHTTPOrgCreator(s Auth0Settings, registry *metricx.Registry) (*HTTPOrgCreator, error) {
	enableOTel := false
	cfg := &httpconfig.HttpConfiguration{
		EnableOpenTelemetry: &enableOTel,
		ServiceConfigs: map[string]httpconfig.ServiceConfig{
			svcAuth0: {BaseURL: s.Domain, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiToken:      {Path: "/oauth/token", Method: "POST"},
				apiCreateOrg:  {Path: "/api/v2/organizations", Method: "POST"},
				apiUserOrgs:   {Path: "/api/v2/users/{id}/organizations", Method: "GET"},
				apiGetUser:    {Path: "/api/v2/users/{id}", Method: "GET"},
				apiAddMembers: {Path: "/api/v2/organizations/{id}/members", Method: "POST"},
				apiAddRoles:   {Path: "/api/v2/organizations/{id}/members/{userId}/roles", Method: "POST"},
				apiDeleteOrg:  {Path: "/api/v2/organizations/{id}", Method: "DELETE"},
			}},
		},
	}
	client, err := hc.NewBureauHttpClient(cfg, registry)
	if err != nil {
		return nil, fmt.Errorf("build auth0 http client: %w", err)
	}
	return &HTTPOrgCreator{http: client, cfg: s}, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type orgResponse struct {
	ID string `json:"id"`
}

// CreateOrganisation ports the auth service's create-organisation flow.
func (c *HTTPOrgCreator) CreateOrganisation(ctx context.Context, in CreateOrgInput) (string, error) {
	if !validDisplayName(in.DisplayName) {
		return "", fmt.Errorf("invalid display name %q", in.DisplayName)
	}

	token, err := c.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	auth := map[string]string{"Authorization": "Bearer " + token}

	// One org per user (auth service is_user_owns_org).
	owns, err := c.userOwnsOrg(ctx, auth, in.UserID)
	if err != nil {
		return "", err
	}
	if owns {
		return "", ErrUserAlreadyOwnsOrg
	}

	// Create the org with a random name + Bureau branding, metadata and both
	// login connections enabled.
	name := "organisation_" + uuid.NewString()
	var created orgResponse
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiCreateOrg, Headers: auth,
		RequestBody: map[string]any{
			"name":         name,
			"display_name": in.DisplayName,
			"branding":     map[string]any{"colors": map[string]any{"primary": brandPrimary, "page_background": brandPageBg}},
			"metadata":     map[string]any{"self_signed_up": selfSignedUp, "isTermsAccepted": in.TncAccepted},
			"enabled_connections": []map[string]any{
				{"connection_id": c.cfg.UsernamePasswordConnectionID, "assign_membership_on_login": false},
				{"connection_id": c.cfg.SSOConnectionID, "assign_membership_on_login": false},
			},
		},
	}, &created); err != nil {
		return "", fmt.Errorf("create organisation: %w", err)
	}

	// Atomic: add the user as a member and grant the owner role. On failure,
	// delete the org so a retry starts clean (auth service handle_create_org_error).
	if err := c.addUserToOrg(ctx, auth, created.ID, in.UserID); err != nil {
		c.deleteOrg(ctx, auth, created.ID) // best-effort cleanup
		return "", err
	}
	return created.ID, nil
}

// addUserToOrg adds the user as an organisation member and assigns the owner role.
func (c *HTTPOrgCreator) addUserToOrg(ctx context.Context, auth map[string]string, orgID, userID string) error {
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiAddMembers, Headers: auth,
		PathParams:  map[string]string{"id": orgID},
		RequestBody: map[string]any{"members": []string{userID}},
	}, nil); err != nil {
		return fmt.Errorf("add user to organisation: %w", err)
	}
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiAddRoles, Headers: auth,
		PathParams:  map[string]string{"id": orgID, "userId": userID},
		RequestBody: map[string]any{"roles": []string{c.cfg.OwnerRoleID}},
	}, nil); err != nil {
		return fmt.Errorf("assign owner role: %w", err)
	}
	return nil
}

// deleteOrg best-effort removes an org during failure cleanup.
func (c *HTTPOrgCreator) deleteOrg(ctx context.Context, auth map[string]string, orgID string) {
	_ = c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiDeleteOrg, Headers: auth,
		PathParams: map[string]string{"id": orgID},
	}, nil)
}

// userOwnsOrg reports whether the user already belongs to any organisation.
func (c *HTTPOrgCreator) userOwnsOrg(ctx context.Context, auth map[string]string, userID string) (bool, error) {
	var orgs []orgResponse
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiUserOrgs, Headers: auth,
		PathParams:  map[string]string{"id": userID},
		QueryParams: map[string]string{"page": "0", "per_page": "10"},
	}, &orgs); err != nil {
		return false, fmt.Errorf("check user organisations: %w", err)
	}
	return len(orgs) > 0, nil
}

type userResponse struct {
	Email string `json:"email"`
}

// UserEmail fetches the user's email from Auth0 (GET /api/v2/users/{id}),
// mirroring the auth service's users.get lookup used for billing.
func (c *HTTPOrgCreator) UserEmail(ctx context.Context, userID string) (string, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	var user userResponse
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiGetUser,
		Headers:    map[string]string{"Authorization": "Bearer " + token},
		PathParams: map[string]string{"id": userID},
	}, &user); err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	return user.Email, nil
}

// ensureToken returns a cached M2M token, fetching a new one when absent/expired.
func (c *HTTPOrgCreator) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.exp) {
		return c.token, nil
	}
	var resp tokenResponse
	if err := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiToken,
		RequestBody: map[string]any{
			"grant_type":    "client_credentials",
			"client_id":     c.cfg.ClientID,
			"client_secret": c.cfg.ClientSecret,
			"audience":      c.cfg.Audience,
		},
	}, &resp); err != nil {
		return "", fmt.Errorf("fetch auth0 m2m token: %w", err)
	}
	c.token = resp.AccessToken
	c.exp = time.Now().Add(time.Duration(resp.ExpiresIn)*time.Second - tokenExpiryBuffer)
	return c.token, nil
}

// displayNameRe encodes the auth service's valid_display_name rule: 2–100 chars,
// unicode letters/numbers with interior spaces, underscores and dashes, starting
// and ending alphanumeric (which also forbids leading/trailing whitespace). Go's
// RE2 has no lookahead, but anchoring the first/last char is equivalent to prod's
// (?!\s)(?!.*\s$) guards.
var displayNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} _-]{0,98}[\p{L}\p{N}]$`)

func validDisplayName(s string) bool {
	return displayNameRe.MatchString(s)
}
