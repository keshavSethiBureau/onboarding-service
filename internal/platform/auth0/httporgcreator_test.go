package auth0

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

// mockAuth0 is a minimal Auth0 Management API covering the org-creation flow:
// token, user's orgs (for the owns-org guard), create org, add member, assign
// role, delete org. Counters + captured bodies let tests assert the sequence.
type mockAuth0 struct {
	mu sync.Mutex

	userOrgs  []string // preloaded orgs the user already belongs to
	failRoles bool     // make the assign-role call fail (to exercise cleanup)

	creates, members, roles, deletes int
	lastCreateBody                   map[string]any
	lastRolesBody                    map[string]any
}

func (m *mockAuth0) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":86400}`))
	})

	// GET /api/v2/users/{id}/organizations
	mux.HandleFunc("/api/v2/users/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			return
		}
		m.mu.Lock()
		orgs := make([]map[string]string, 0, len(m.userOrgs))
		for _, id := range m.userOrgs {
			orgs = append(orgs, map[string]string{"id": id})
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(orgs)
	})

	// POST /api/v2/organizations (exact) — create
	mux.HandleFunc("/api/v2/organizations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&m.lastCreateBody)
		m.creates++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"org_created_1"}`))
	})

	// /api/v2/organizations/{id}[/members[/{userId}/roles]] — members, roles, delete
	mux.HandleFunc("/api/v2/organizations/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case r.Method == http.MethodDelete:
			m.deletes++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/roles"):
			_ = json.NewDecoder(r.Body).Decode(&m.lastRolesBody)
			m.roles++
			if m.failRoles {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/members"):
			m.members++
			w.WriteHeader(http.StatusNoContent)
		default:
			// ignore HEAD warmup probes / anything else
		}
	})

	return mux
}

func testSettings(url string) Auth0Settings {
	return Auth0Settings{
		Domain:                       url,
		ClientID:                     "cid",
		ClientSecret:                 "secret",
		Audience:                     "https://api/",
		UsernamePasswordConnectionID: "con_userpass",
		SSOConnectionID:              "con_sso",
		OwnerRoleID:                  "role_owner",
	}
}

func newTestOrgCreator(t *testing.T, url string) *HTTPOrgCreator {
	t.Helper()
	c, err := NewHTTPOrgCreator(testSettings(url), metricx.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPOrgCreator: %v", err)
	}
	return c
}

// TestHTTPOrgCreator_CreateFlow proves the full prod-faithful sequence: create
// org (with connections/branding/metadata), add member, assign owner role.
func TestHTTPOrgCreator_CreateFlow(t *testing.T) {
	mock := &mockAuth0{}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c := newTestOrgCreator(t, srv.URL)
	id, err := c.CreateOrganisation(context.Background(), CreateOrgInput{
		UserID: "auth0|abc123", DisplayName: "Acme Inc", TncAccepted: "true",
	})
	if err != nil {
		t.Fatalf("CreateOrganisation: %v", err)
	}
	if id != "org_created_1" {
		t.Fatalf("org id = %q, want org_created_1", id)
	}
	if mock.creates != 1 || mock.members != 1 || mock.roles != 1 || mock.deletes != 0 {
		t.Fatalf("sequence = creates:%d members:%d roles:%d deletes:%d, want 1/1/1/0",
			mock.creates, mock.members, mock.roles, mock.deletes)
	}

	// Create body carries both connections, branding and metadata.
	conns, _ := mock.lastCreateBody["enabled_connections"].([]any)
	if len(conns) != 2 {
		t.Fatalf("enabled_connections = %v, want 2", mock.lastCreateBody["enabled_connections"])
	}
	meta, _ := mock.lastCreateBody["metadata"].(map[string]any)
	if meta["isTermsAccepted"] != "true" || meta["self_signed_up"] != "True" {
		t.Errorf("metadata = %v, want isTermsAccepted=true self_signed_up=True", meta)
	}
	if mock.lastCreateBody["branding"] == nil {
		t.Error("branding missing from create body")
	}
	// Owner role assigned from config.
	if roles, _ := mock.lastRolesBody["roles"].([]any); len(roles) != 1 || roles[0] != "role_owner" {
		t.Errorf("roles body = %v, want [role_owner]", mock.lastRolesBody["roles"])
	}
}

// TestHTTPOrgCreator_RejectsWhenUserOwnsOrg mirrors is_user_owns_org: a user who
// already belongs to an org is rejected, and no org is created.
func TestHTTPOrgCreator_RejectsWhenUserOwnsOrg(t *testing.T) {
	mock := &mockAuth0{userOrgs: []string{"org_existing"}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c := newTestOrgCreator(t, srv.URL)
	_, err := c.CreateOrganisation(context.Background(), CreateOrgInput{
		UserID: "auth0|abc123", DisplayName: "Acme Inc", TncAccepted: "true",
	})
	if !errors.Is(err, ErrUserAlreadyOwnsOrg) {
		t.Fatalf("err = %v, want ErrUserAlreadyOwnsOrg", err)
	}
	if mock.creates != 0 {
		t.Fatalf("creates = %d, want 0 (guard should short-circuit)", mock.creates)
	}
}

// TestHTTPOrgCreator_DeletesOnFailure proves the atomic cleanup: if assigning the
// owner role fails, the just-created org is deleted and an error is returned.
func TestHTTPOrgCreator_DeletesOnFailure(t *testing.T) {
	mock := &mockAuth0{failRoles: true}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c := newTestOrgCreator(t, srv.URL)
	_, err := c.CreateOrganisation(context.Background(), CreateOrgInput{
		UserID: "auth0|abc123", DisplayName: "Acme Inc", TncAccepted: "true",
	})
	if err == nil {
		t.Fatal("expected error when role assignment fails")
	}
	if mock.creates != 1 || mock.deletes != 1 {
		t.Fatalf("creates:%d deletes:%d, want 1/1 (org must be cleaned up)", mock.creates, mock.deletes)
	}
}

func TestValidDisplayName(t *testing.T) {
	valid := []string{"Ab", "Acme Inc", "Bureau_123", "a-b c", "Åcmé 2"}
	invalid := []string{"", "a", " Acme", "Acme ", "a<b>", "x=y", strings.Repeat("a", 101)}
	for _, s := range valid {
		if !validDisplayName(s) {
			t.Errorf("validDisplayName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validDisplayName(s) {
			t.Errorf("validDisplayName(%q) = true, want false", s)
		}
	}
}
