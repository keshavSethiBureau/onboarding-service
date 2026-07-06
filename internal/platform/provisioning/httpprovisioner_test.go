package provisioning

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
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

func testSettings(serverURL string) Settings {
	return Settings{
		SvixBaseURL: serverURL, SvixToken: "svixtok",
		LagoBaseURL: serverURL, LagoToken: "lagotok",
		KongBaseURL: serverURL, KongToken: "kongtok",
		Environment: "test",
	}
}

// newTestProvisioner points all HTTP services at one mock server and stubs AWS.
func newTestProvisioner(t *testing.T, serverURL string) *HTTPProvisioner {
	t.Helper()
	s := testSettings(serverURL)
	client, err := buildHTTPClient(s, metricx.NewRegistry())
	if err != nil {
		t.Fatalf("buildHTTPClient: %v", err)
	}
	return &HTTPProvisioner{http: client, cfg: s, aws: fakeAWS{id: "aws_key"}}
}

// TestHTTPProvisioner_StatusHandling proves each HTTP provisioner: 2xx succeeds,
// 409 is treated as idempotent success, and 5xx surfaces an error.
func TestHTTPProvisioner_StatusHandling(t *testing.T) {
	in := ProvisionInput{OrgID: "org1", DisplayName: "Acme", Email: "u@e.com"}
	calls := map[string]func(p *HTTPProvisioner) (string, error){
		"svix": func(p *HTTPProvisioner) (string, error) { return p.Svix(context.Background(), in) },
		"lago": func(p *HTTPProvisioner) (string, error) { return p.Lago(context.Background(), in) },
		"kong": func(p *HTTPProvisioner) (string, error) { return p.Kong(context.Background(), in) },
	}

	for name, call := range calls {
		for _, tc := range []struct {
			status  int
			wantErr bool
		}{
			{http.StatusCreated, false},
			{http.StatusConflict, false}, // already exists -> idempotent success
			{http.StatusInternalServerError, true},
		} {
			t.Run(name+"-"+http.StatusText(tc.status), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"id":"x"}`))
				}))
				defer srv.Close()

				p := newTestProvisioner(t, srv.URL)
				_, err := call(p)
				if tc.wantErr && err == nil {
					t.Fatalf("%s status %d: expected error, got nil", name, tc.status)
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("%s status %d: unexpected error: %v", name, tc.status, err)
				}
			})
		}
	}
}

// captureServer records POST request bodies + auth headers per path.
type captureServer struct {
	mu   sync.Mutex
	reqs map[string][]capturedReq
}

type capturedReq struct {
	auth string
	body map[string]any
}

func (c *captureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost { // ignore httpclient HEAD warmup probes
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		if c.reqs == nil {
			c.reqs = map[string][]capturedReq{}
		}
		c.reqs[r.URL.Path] = append(c.reqs[r.URL.Path], capturedReq{auth: r.Header.Get("Authorization"), body: body})
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	})
}

// firstReq returns the first captured request whose path contains substr (paths
// are matched loosely because the httpclient may normalise trailing slashes).
func (c *captureServer) firstReq(t *testing.T, substr string) capturedReq {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for path, reqs := range c.reqs {
		if strings.Contains(path, substr) && len(reqs) > 0 {
			return reqs[0]
		}
	}
	t.Fatalf("no captured request matching %q", substr)
	return capturedReq{}
}

// countReq counts captured requests whose path contains substr.
func (c *captureServer) countReq(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for path, reqs := range c.reqs {
		if strings.Contains(path, substr) {
			n += len(reqs)
		}
	}
	return n
}

// TestHTTPProvisioner_Payloads asserts the request shapes match the auth service.
func TestHTTPProvisioner_Payloads(t *testing.T) {
	cs := &captureServer{}
	srv := httptest.NewServer(cs.handler())
	defer srv.Close()
	p := newTestProvisioner(t, srv.URL)
	in := ProvisionInput{OrgID: "org1", DisplayName: "Acme Inc", Email: "u@e.com"}
	ctx := context.Background()

	// Kong: authenticated, username/custom_id = orgId, tags = [displayName, env].
	if _, err := p.Kong(ctx, in); err != nil {
		t.Fatalf("Kong: %v", err)
	}
	kong := cs.firstReq(t, "consumers")
	if kong.auth != "Bearer kongtok" {
		t.Errorf("Kong auth = %q, want Bearer kongtok", kong.auth)
	}
	if kong.body["username"] != "org1" || kong.body["custom_id"] != "org1" {
		t.Errorf("Kong username/custom_id = %v/%v, want org1", kong.body["username"], kong.body["custom_id"])
	}
	if tags := toStrings(kong.body["tags"]); len(tags) != 2 || tags[0] != "Acme Inc" || tags[1] != "test" {
		t.Errorf("Kong tags = %v, want [Acme Inc test]", tags)
	}

	// Svix: name = displayName (not orgId).
	if _, err := p.Svix(ctx, in); err != nil {
		t.Fatalf("Svix: %v", err)
	}
	svix := cs.firstReq(t, "app")
	if svix.auth != "Bearer svixtok" {
		t.Errorf("Svix auth = %q", svix.auth)
	}
	if svix.body["uid"] != "org1" || svix.body["name"] != "Acme Inc" {
		t.Errorf("Svix uid/name = %v/%v, want org1 / Acme Inc", svix.body["uid"], svix.body["name"])
	}

	// Lago: rich customer + 3 plans + 3 subscriptions.
	if _, err := p.Lago(ctx, in); err != nil {
		t.Fatalf("Lago: %v", err)
	}
	cust := cs.firstReq(t, "customers").body["customer"].(map[string]any)
	if cust["email"] != "u@e.com" || cust["currency"] != "INR" || cust["name"] != "Acme Inc" {
		t.Errorf("Lago customer = %v", cust)
	}
	if cust["billing_configuration"] == nil {
		t.Error("Lago customer missing billing_configuration (Stripe)")
	}
	if got := cs.countReq("plans"); got != 3 {
		t.Errorf("Lago plans = %d, want 3", got)
	}
	if got := cs.countReq("subscriptions"); got != 3 {
		t.Errorf("Lago subscriptions = %d, want 3", got)
	}
	if code := cs.firstReq(t, "plans").body["plan"].(map[string]any)["code"]; code != "d_c_plan_org1" {
		t.Errorf("first plan code = %v, want d_c_plan_org1", code)
	}
}

func toStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i], _ = e.(string)
	}
	return out
}

// capturingAPIGateway records the CreateApiKeyInput so tests can assert its fields.
type capturingAPIGateway struct {
	createErr error
	lastInput *apigateway.CreateApiKeyInput
}

func (f *capturingAPIGateway) CreateApiKey(_ context.Context, in *apigateway.CreateApiKeyInput, _ ...func(*apigateway.Options)) (*apigateway.CreateApiKeyOutput, error) {
	f.lastInput = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := "key123"
	return &apigateway.CreateApiKeyOutput{Id: &id}, nil
}

func (f *capturingAPIGateway) CreateUsagePlanKey(context.Context, *apigateway.CreateUsagePlanKeyInput, ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanKeyOutput, error) {
	return &apigateway.CreateUsagePlanKeyOutput{}, nil
}

func TestAWSGateway_EnsureAPIKey(t *testing.T) {
	t.Run("creates key with deterministic value + description, no customerId", func(t *testing.T) {
		fake := &capturingAPIGateway{}
		g := &awsGateway{client: fake, usagePlanID: "up1"}
		id, err := g.ensureAPIKey(context.Background(), "org1", "Acme")
		if err != nil || id != "key123" {
			t.Fatalf("id=%q err=%v", id, err)
		}
		if v := aws.ToString(fake.lastInput.Value); v != "org1_key_svc_random" {
			t.Errorf("api key value = %q, want org1_key_svc_random", v)
		}
		if d := aws.ToString(fake.lastInput.Description); d == "" {
			t.Error("api key description empty")
		}
		if fake.lastInput.CustomerId != nil {
			t.Errorf("customerId set (%q), want nil (prod does not set it)", aws.ToString(fake.lastInput.CustomerId))
		}
	})

	t.Run("conflict is idempotent success", func(t *testing.T) {
		g := &awsGateway{client: &capturingAPIGateway{createErr: &agtypes.ConflictException{}}, usagePlanID: "up1"}
		id, err := g.ensureAPIKey(context.Background(), "org1", "Acme")
		if err != nil {
			t.Fatalf("conflict should be idempotent success, got %v", err)
		}
		if id != "org1" {
			t.Errorf("id = %q, want org1", id)
		}
	})

	t.Run("other error surfaces", func(t *testing.T) {
		g := &awsGateway{client: &capturingAPIGateway{createErr: errors.New("boom")}, usagePlanID: "up1"}
		if _, err := g.ensureAPIKey(context.Background(), "org1", "Acme"); err == nil {
			t.Fatal("expected error")
		}
	})
}

// fakeAWS satisfies the HTTPProvisioner AWS seam for the HTTP status tests.
type fakeAWS struct{ id string }

func (f fakeAWS) ensureAPIKey(context.Context, string, string) (string, error) { return f.id, nil }
