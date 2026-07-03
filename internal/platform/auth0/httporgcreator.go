package auth0

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	hc "github.com/Bureau-Inc/bureau-commons-go/httpclient"
	httpconfig "github.com/Bureau-Inc/bureau-commons-go/httpclient/config"
	"github.com/Bureau-Inc/bureau-commons-go/httpclient/dtos"
	httperr "github.com/Bureau-Inc/bureau-commons-go/httpclient/errors"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

const (
	svcAuth0 = "auth0mgmt"

	apiToken        = "getToken"
	apiCreateOrg    = "createOrg"
	apiGetOrgByName = "getOrgByName"

	tokenExpiryBuffer = 60 * time.Second
)

// HTTPOrgCreator creates Auth0 organisations via the Management API over commons
// httpclient. It is idempotent by userId: the org name is a deterministic slug
// of the userId (Auth0 names are unique per tenant), and a 409 on create falls
// back to reading the existing org by name.
type HTTPOrgCreator struct {
	http         *hc.BureauHttpClient
	domain       string
	clientID     string
	clientSecret string

	mu    sync.Mutex
	token string
	exp   time.Time
}

// NewHTTPOrgCreator builds the Auth0 Management org creator. Constructs fine with
// empty config (calls will fail until real credentials are set).
func NewHTTPOrgCreator(domain, clientID, clientSecret string, registry *metricx.Registry) (*HTTPOrgCreator, error) {
	enableOTel := false
	cfg := &httpconfig.HttpConfiguration{
		EnableOpenTelemetry: &enableOTel,
		ServiceConfigs: map[string]httpconfig.ServiceConfig{
			svcAuth0: {BaseURL: domain, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiToken:        {Path: "/oauth/token", Method: "POST"},
				apiCreateOrg:    {Path: "/api/v2/organizations", Method: "POST"},
				apiGetOrgByName: {Path: "/api/v2/organizations/name/{name}", Method: "GET"},
			}},
		},
	}
	client, err := hc.NewBureauHttpClient(cfg, registry)
	if err != nil {
		return nil, fmt.Errorf("build auth0 http client: %w", err)
	}
	return &HTTPOrgCreator{http: client, domain: domain, clientID: clientID, clientSecret: clientSecret}, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type orgResponse struct {
	ID string `json:"id"`
}

// CreateOrganisation creates (or returns the existing) org for the user.
func (c *HTTPOrgCreator) CreateOrganisation(ctx context.Context, userID, displayName string) (string, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	name := orgName(userID)
	auth := map[string]string{"Authorization": "Bearer " + token}

	var created orgResponse
	err = c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
		ServiceName: svcAuth0, ApiName: apiCreateOrg, Headers: auth,
		RequestBody: map[string]any{"name": name, "display_name": displayName},
	}, &created)
	if err == nil {
		return created.ID, nil
	}

	// Name already exists (idempotent): read the org by its stable name.
	var clientErr *httperr.ClientError
	if errors.As(err, &clientErr) && clientErr.StatusCode == 409 {
		var existing orgResponse
		if gerr := c.http.ExecuteWithContext(ctx, &dtos.ApiRequest{
			ServiceName: svcAuth0, ApiName: apiGetOrgByName, Headers: auth,
			PathParams: map[string]string{"name": name},
		}, &existing); gerr != nil {
			return "", fmt.Errorf("get existing org by name: %w", gerr)
		}
		return existing.ID, nil
	}
	return "", fmt.Errorf("create organisation: %w", err)
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
			"client_id":     c.clientID,
			"client_secret": c.clientSecret,
			"audience":      strings.TrimRight(c.domain, "/") + "/api/v2/",
		},
	}, &resp); err != nil {
		return "", fmt.Errorf("fetch auth0 m2m token: %w", err)
	}
	c.token = resp.AccessToken
	c.exp = time.Now().Add(time.Duration(resp.ExpiresIn)*time.Second - tokenExpiryBuffer)
	return c.token, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// orgName derives a deterministic, Auth0-valid org name from the userId. The
// userId is the sole dedup key (no randomness), so retries reuse the same name.
func orgName(userID string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(userID), "-")
	s = strings.Trim(s, "-")
	name := "org-" + s
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}
