package auth0

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

// mockAuth0 is a minimal Auth0 Management API: issues a token, creates an org
// once per name (409 on a repeat), and serves get-by-name.
type mockAuth0 struct {
	mu       sync.Mutex
	byName   map[string]string // name -> id
	creates  int
	tokenGET int
}

func (m *mockAuth0) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.tokenGET++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":86400}`))
	})
	mux.HandleFunc("/api/v2/organizations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // ignore httpclient HEAD warmup probes
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, exists := m.byName[body.Name]; exists {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"statusCode":409,"error":"Conflict"}`))
			return
		}
		m.creates++
		id := "org_id_" + body.Name
		m.byName[body.Name] = id
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
	})
	mux.HandleFunc("/api/v2/organizations/name/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len("/api/v2/organizations/name/"):]
		m.mu.Lock()
		id := m.byName[name]
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
	})
	return mux
}

func newTestOrgCreator(t *testing.T, url string) *HTTPOrgCreator {
	t.Helper()
	c, err := NewHTTPOrgCreator(url, "cid", "secret", metricx.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPOrgCreator: %v", err)
	}
	return c
}

func TestHTTPOrgCreator_CreatesOnce(t *testing.T) {
	mock := &mockAuth0{byName: map[string]string{}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c := newTestOrgCreator(t, srv.URL)
	id, err := c.CreateOrganisation(context.Background(), "auth0|abc123", "Acme Inc")
	if err != nil {
		t.Fatalf("CreateOrganisation: %v", err)
	}
	if id == "" {
		t.Fatal("empty org id")
	}
	if mock.creates != 1 {
		t.Fatalf("creates = %d, want 1", mock.creates)
	}
}

// TestHTTPOrgCreator_Idempotent proves a repeat call for the same user does NOT
// create a second org: the 409 on the stable name falls back to get-by-name and
// returns the same id.
func TestHTTPOrgCreator_Idempotent(t *testing.T) {
	mock := &mockAuth0{byName: map[string]string{}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	c := newTestOrgCreator(t, srv.URL)
	ctx := context.Background()

	id1, err := c.CreateOrganisation(ctx, "auth0|abc123", "Acme Inc")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// A different HTTPOrgCreator (fresh token cache) simulates a retry/replay on
	// another worker; same userId -> same org, no second create.
	c2 := newTestOrgCreator(t, srv.URL)
	id2, err := c2.CreateOrganisation(ctx, "auth0|abc123", "Acme Renamed")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ (%q != %q) — duplicate org created", id1, id2)
	}
	if mock.creates != 1 {
		t.Fatalf("creates = %d, want exactly 1 (idempotent by userId)", mock.creates)
	}
}

func TestOrgName_Deterministic(t *testing.T) {
	if a, b := orgName("auth0|Abc 123"), orgName("auth0|Abc 123"); a != b {
		t.Fatalf("non-deterministic: %q vs %q", a, b)
	}
	if got := orgName("auth0|abc"); got != "org-auth0-abc" {
		t.Errorf("orgName = %q, want org-auth0-abc", got)
	}
}
