package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestInternalTokenMiddleware verifies the internal-only guard: with a shared
// secret configured, non-internal callers (missing/wrong token) are rejected;
// only a matching token passes. Empty token is dev-open.
func TestInternalTokenMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		token      string
		header     string
		wantStatus int
	}{
		{name: "token set, no header -> rejected", token: "s3cret", header: "", wantStatus: http.StatusUnauthorized},
		{name: "token set, wrong header -> rejected", token: "s3cret", header: "nope", wantStatus: http.StatusUnauthorized},
		{name: "token set, correct header -> allowed", token: "s3cret", header: "s3cret", wantStatus: http.StatusOK},
		{name: "no token (dev) -> allowed", token: "", header: "", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(InternalTokenMiddleware(tt.token))
			r.POST("/v1/internal/onboarding/steps", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/internal/onboarding/steps", nil)
			if tt.header != "" {
				req.Header.Set("X-Internal-Auth-Token", tt.header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
