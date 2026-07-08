// REMOVED(single-entry): the entire Auth /me client is dead. This service makes
// ZERO calls to the Auth Service in any direction — /me is never called, proxied
// or reimplemented. The implementation is retained (commented) per the removal
// convention; nothing imports this package.
package authsvc

/*
import (
	"context"
	"errors"
	"fmt"
	"time"

	hc "github.com/Bureau-Inc/bureau-commons-go/httpclient"
	httpconfig "github.com/Bureau-Inc/bureau-commons-go/httpclient/config"
	"github.com/Bureau-Inc/bureau-commons-go/httpclient/dtos"
	httperr "github.com/Bureau-Inc/bureau-commons-go/httpclient/errors"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

const (
	svcAuth = "authsvc"
	apiMe   = "getMe"
)

// ErrAuthUnavailable is returned when /me could not be reached after the bounded
// retries (timeout / 5xx / transport error). It is RETRYABLE: the caller should
// surface a retryable status and start NO journey, so a later retry (or the
// login /state entry) completes cleanly.
var ErrAuthUnavailable = errors.New("auth service /me unavailable")

// MeInfo is the subset of the Auth Service's /me response. The onboarding
// journey consumes none of these fields (identity comes from the validated
// token); the call exists to confirm the caller against Auth at signup. Unknown
// fields are ignored on decode, so this stays forward-compatible.
type MeInfo struct {
	Permissions   []string `json:"permissions"`
	IsLiveEnabled bool     `json:"isLiveEnabled"`
	Industry      string   `json:"industry"`
	Geography     string   `json:"geography"`
	Region        string   `json:"region"`
}

// MeClient calls the Auth Service's /me. Implemented by HTTPMeClient; faked in
// tests.
type MeClient interface {
	Me(ctx context.Context, bearerToken string) (*MeInfo, error)
}

// Settings configures the /me HTTP client: the Auth Service base URL plus the
// bounded timeout/retry policy that keeps signup safe when Auth is slow/down.
type Settings struct {
	BaseURL           string
	Attempts          int           // total attempts (>=1); bounds the retry loop
	PerAttemptTimeout time.Duration // per-call deadline
	Backoff           time.Duration // wait between attempts
}

func (s Settings) withDefaults() Settings {
	if s.Attempts < 1 {
		s.Attempts = 3
	}
	if s.PerAttemptTimeout <= 0 {
		s.PerAttemptTimeout = 2 * time.Second
	}
	if s.Backoff <= 0 {
		s.Backoff = 200 * time.Millisecond
	}
	return s
}

// HTTPMeClient calls GET {baseURL}/me over the commons httpclient, forwarding the
// caller's bearer token verbatim (it never re-validates or reimplements /me).
type HTTPMeClient struct {
	http *hc.BureauHttpClient
	cfg  Settings
}

// NewHTTPMeClient builds the /me client. It constructs fine with an empty base
// URL (calls then fail until a real URL is configured).
func NewHTTPMeClient(s Settings, registry *metricx.Registry) (*HTTPMeClient, error) {
	s = s.withDefaults()
	enableOTel := false
	cfg := &httpconfig.HttpConfiguration{
		EnableOpenTelemetry: &enableOTel,
		ServiceConfigs: map[string]httpconfig.ServiceConfig{
			svcAuth: {BaseURL: s.BaseURL, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiMe: {Path: "/me", Method: "GET"},
			}},
		},
	}
	client, err := hc.NewBureauHttpClient(cfg, registry)
	if err != nil {
		return nil, fmt.Errorf("build auth /me http client: %w", err)
	}
	return &HTTPMeClient{http: client, cfg: s}, nil
}

// Me calls GET /me forwarding the bearer token. A 4xx (the token was rejected by
// Auth) is returned as-is and is NOT retried — a retry can't fix a bad token. A
// timeout / 5xx / transport error is retried up to Attempts; on exhaustion it
// returns ErrAuthUnavailable (retryable).
func (c *HTTPMeClient) Me(ctx context.Context, bearerToken string) (*MeInfo, error) {
	req := &dtos.ApiRequest{
		ServiceName: svcAuth,
		ApiName:     apiMe,
		Headers:     map[string]string{"Authorization": bearerToken},
	}
	var last error
	for attempt := 0; attempt < c.cfg.Attempts; attempt++ {
		var info MeInfo
		attemptCtx, cancel := context.WithTimeout(ctx, c.cfg.PerAttemptTimeout)
		err := c.http.ExecuteWithContext(attemptCtx, req, &info)
		cancel()
		if err == nil {
			return &info, nil
		}
		var clientErr *httperr.ClientError
		if errors.As(err, &clientErr) && clientErr.StatusCode >= 400 && clientErr.StatusCode < 500 {
			return nil, fmt.Errorf("auth /me rejected the request (status %d): %w", clientErr.StatusCode, err)
		}
		last = err
		if attempt < c.cfg.Attempts-1 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, ctx.Err())
			case <-time.After(c.cfg.Backoff):
			}
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, last)
}
*/
