// Package auth provides the Auth0 JWT authentication middleware and helpers to
// read the caller's identity from the request context. Identity (userId, orgId)
// always comes from the token, never the request body (LLD §2.6).
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Context keys under which the middleware stores the caller identity.
const (
	CtxUserID = "auth.userID"
	CtxOrgID  = "auth.orgID"
)

// Config configures the Auth0 validator.
type Config struct {
	Enabled  bool
	Issuer   string
	Audience string
	JWKSURL  string
}

// onboardingClaims are the token claims we consume: sub -> userId (RegisteredClaims)
// and the Auth0 org token's org_id -> orgId.
type onboardingClaims struct {
	OrgID string `json:"org_id"`
	jwt.RegisteredClaims
}

// Middleware validates Auth0 JWTs and injects identity into the request context.
type Middleware struct {
	enabled  bool
	issuer   string
	audience string
	keyfunc  jwt.Keyfunc
}

// New builds the middleware. When disabled it performs no JWKS fetch and instead
// reads identity from X-User-Id/X-Org-Id headers (local dev). When enabled it
// fetches the JWKS from cfg.JWKSURL (with background rotation) and validates RS256.
func New(cfg Config) (*Middleware, error) {
	m := &Middleware{enabled: cfg.Enabled, issuer: cfg.Issuer, audience: cfg.Audience}
	if !cfg.Enabled {
		return m, nil
	}
	if cfg.JWKSURL == "" {
		return nil, errors.New("auth enabled but jwksUrl is empty")
	}
	jwks, err := keyfunc.NewDefault([]string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("init auth0 jwks: %w", err)
	}
	m.keyfunc = jwks.Keyfunc
	return m, nil
}

// Handler is the Gin middleware.
func (m *Middleware) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.enabled {
			m.handleDisabled(c)
			return
		}

		raw, ok := bearerToken(c)
		if !ok {
			abort(c, "missing or malformed bearer token")
			return
		}

		claims := &onboardingClaims{}
		token, err := jwt.ParseWithClaims(raw, claims, m.keyfunc,
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithIssuer(m.issuer),
			jwt.WithAudience(m.audience),
		)
		if err != nil || !token.Valid {
			abort(c, "invalid token")
			return
		}
		if claims.Subject == "" {
			abort(c, "token missing sub claim")
			return
		}

		setIdentity(c, claims.Subject, claims.OrgID)
		c.Next()
	}
}

// handleDisabled is the local-dev path: identity comes from headers.
func (m *Middleware) handleDisabled(c *gin.Context) {
	userID := c.GetHeader("X-User-Id")
	if userID == "" {
		abort(c, "missing X-User-Id header (auth disabled dev mode)")
		return
	}
	setIdentity(c, userID, c.GetHeader("X-Org-Id"))
	c.Next()
}

// Identity returns the caller's userId and orgId from the context. ok is false
// if no authenticated identity is present.
func Identity(c *gin.Context) (userID, orgID string, ok bool) {
	v, exists := c.Get(CtxUserID)
	if !exists {
		return "", "", false
	}
	userID, _ = v.(string)
	if o, exists := c.Get(CtxOrgID); exists {
		orgID, _ = o.(string)
	}
	return userID, orgID, userID != ""
}

func setIdentity(c *gin.Context, userID, orgID string) {
	c.Set(CtxUserID, userID)
	c.Set(CtxOrgID, orgID)
}

func bearerToken(c *gin.Context) (string, bool) {
	const prefix = "Bearer "
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return tok, tok != ""
}

func abort(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": msg})
}
