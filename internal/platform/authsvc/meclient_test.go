package authsvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

func newTestMeClient(t *testing.T, baseURL string) *HTTPMeClient {
	t.Helper()
	c, err := NewHTTPMeClient(Settings{
		BaseURL: baseURL, Attempts: 3, PerAttemptTimeout: time.Second, Backoff: time.Millisecond,
	}, metricx.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPMeClient: %v", err)
	}
	return c
}

// TestMe_Success returns the decoded /me payload on 200.
func TestMe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet { // ignore the client's async HEAD warmup probe
			return
		}
		if r.URL.Path != "/me" {
			t.Errorf("path = %q, want /me", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("bearer not forwarded: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"isLiveEnabled":true,"industry":"fintech"}`))
	}))
	defer srv.Close()

	info, err := newTestMeClient(t, srv.URL).Me(context.Background(), "Bearer tok")
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if !info.IsLiveEnabled || info.Industry != "fintech" {
		t.Errorf("decoded info = %+v", info)
	}
}

// TestMe_5xxRetriesThenRetryable proves a 5xx is retried up to Attempts and then
// surfaces ErrAuthUnavailable (retryable) — the signup-safety property.
func TestMe_5xxRetriesThenRetryable(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet { // ignore async HEAD warmup
			return
		}
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestMeClient(t, srv.URL).Me(context.Background(), "Bearer tok")
	if !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("err = %v, want ErrAuthUnavailable", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("server hits = %d, want 3 (bounded retries)", got)
	}
}

// TestMe_4xxNotRetried proves a 4xx (Auth rejected the token) fails fast and is
// NOT retried and NOT classified as retryable.
func TestMe_4xxNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet { // ignore async HEAD warmup
			return
		}
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newTestMeClient(t, srv.URL).Me(context.Background(), "Bearer tok")
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if errors.Is(err, ErrAuthUnavailable) {
		t.Errorf("4xx must not be retryable: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (no retry on 4xx)", got)
	}
}
