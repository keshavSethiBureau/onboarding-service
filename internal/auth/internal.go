package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// InternalTokenMiddleware guards the internal-only endpoints. When token is set,
// requests must carry a matching X-Internal-Auth-Token header; when empty
// (local dev), it is a no-op and network isolation is the real boundary.
func InternalTokenMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		if c.GetHeader("X-Internal-Auth-Token") != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid internal auth token"})
			return
		}
		c.Next()
	}
}
