// Package controller holds the HTTP handlers and route registration.
package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// readinessTimeout bounds the dependency check behind GET /ready.
const readinessTimeout = 2 * time.Second

// HealthController serves the liveness and readiness probes.
//
//   - /health is liveness: 200 as long as the process is up.
//   - /ready  is readiness: 200 only when dependencies (currently MongoDB) are
//     reachable, 503 otherwise — see agent_docs/onboarding-lld.md §9.
type HealthController struct {
	// checkReady reports whether downstream dependencies are reachable. A nil
	// check means the service is always considered ready.
	checkReady func(context.Context) error
}

// NewHealthController returns a HealthController whose readiness probe uses
// checkReady (e.g. a MongoDB ping). Pass nil for an always-ready service.
func NewHealthController(checkReady func(context.Context) error) *HealthController {
	return &HealthController{checkReady: checkReady}
}

// RegisterRoutes wires the health endpoints onto the router.
func (c *HealthController) RegisterRoutes(r gin.IRouter) {
	r.GET("/health", c.Health)
	r.GET("/ready", c.Ready)
}

// Health responds 200 OK to indicate the process is alive.
func (c *HealthController) Health(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready responds 200 when dependencies are reachable, 503 otherwise.
func (c *HealthController) Ready(ctx *gin.Context) {
	if c.checkReady == nil {
		ctx.JSON(http.StatusOK, gin.H{"status": "ready"})
		return
	}

	cctx, cancel := context.WithTimeout(ctx.Request.Context(), readinessTimeout)
	defer cancel()

	if err := c.checkReady(cctx); err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "mongo": "down"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"status": "ready", "mongo": "ok"})
}
