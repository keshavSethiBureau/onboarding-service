package provisioning

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

// newTestProvisioner points all HTTP services at one mock server and stubs AWS.
func newTestProvisioner(t *testing.T, serverURL string) *HTTPProvisioner {
	t.Helper()
	s := Settings{
		SvixBaseURL: serverURL, SvixToken: "tok",
		LagoBaseURL: serverURL, LagoToken: "tok",
		KongBaseURL: serverURL,
	}
	client, err := buildHTTPClient(s, metricx.NewRegistry())
	if err != nil {
		t.Fatalf("buildHTTPClient: %v", err)
	}
	return &HTTPProvisioner{http: client, cfg: s, aws: fakeAWS{id: "aws_key"}}
}

// TestHTTPProvisioner_StatusHandling proves each HTTP provisioner: 2xx succeeds,
// 409 is treated as idempotent success, and 5xx surfaces an error.
func TestHTTPProvisioner_StatusHandling(t *testing.T) {
	calls := map[string]func(p *HTTPProvisioner) (string, error){
		"svix": func(p *HTTPProvisioner) (string, error) { return p.Svix(context.Background(), "org1", "Acme") },
		"lago": func(p *HTTPProvisioner) (string, error) { return p.Lago(context.Background(), "org1", "Acme") },
		"kong": func(p *HTTPProvisioner) (string, error) { return p.Kong(context.Background(), "org1", "Acme") },
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

// fakeAPIGateway lets us test awsGateway.ensureAPIKey idempotency without AWS.
type fakeAPIGateway struct {
	createErr error
}

func (f fakeAPIGateway) CreateApiKey(context.Context, *apigateway.CreateApiKeyInput, ...func(*apigateway.Options)) (*apigateway.CreateApiKeyOutput, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := "key123"
	return &apigateway.CreateApiKeyOutput{Id: &id}, nil
}

func (f fakeAPIGateway) CreateUsagePlanKey(context.Context, *apigateway.CreateUsagePlanKeyInput, ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanKeyOutput, error) {
	return &apigateway.CreateUsagePlanKeyOutput{}, nil
}

func TestAWSGateway_EnsureAPIKey(t *testing.T) {
	t.Run("creates key and attaches plan", func(t *testing.T) {
		g := &awsGateway{client: fakeAPIGateway{}, usagePlanID: "up1"}
		id, err := g.ensureAPIKey(context.Background(), "org1", "Acme")
		if err != nil || id != "key123" {
			t.Fatalf("id=%q err=%v", id, err)
		}
	})

	t.Run("conflict is idempotent success", func(t *testing.T) {
		g := &awsGateway{client: fakeAPIGateway{createErr: &agtypes.ConflictException{}}, usagePlanID: "up1"}
		id, err := g.ensureAPIKey(context.Background(), "org1", "Acme")
		if err != nil {
			t.Fatalf("conflict should be idempotent success, got %v", err)
		}
		if id != "org1" {
			t.Errorf("id = %q, want org1", id)
		}
	})

	t.Run("other error surfaces", func(t *testing.T) {
		g := &awsGateway{client: fakeAPIGateway{createErr: errors.New("boom")}, usagePlanID: "up1"}
		if _, err := g.ensureAPIKey(context.Background(), "org1", "Acme"); err == nil {
			t.Fatal("expected error")
		}
	})
}

// fakeAWS satisfies the HTTPProvisioner AWS seam for the HTTP status tests.
type fakeAWS struct{ id string }

func (f fakeAWS) ensureAPIKey(context.Context, string, string) (string, error) { return f.id, nil }
