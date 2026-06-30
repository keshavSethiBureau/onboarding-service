// Package controller holds the HTTP handlers and route registration.
package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthController serves the liveness probe.
//
// This is liveness only (process is up). Readiness (Mongo connected AND the
// vertical cache warm) is deferred until those dependencies exist — see
// agent_docs/onboarding-lld.md §9.
type HealthController struct{}

// NewHealthController returns a HealthController.
func NewHealthController() *HealthController {
	return &HealthController{}
}

// RegisterRoutes wires the health endpoints onto the router.
func (c *HealthController) RegisterRoutes(r gin.IRouter) {
	r.GET("/health", c.Health)
}

// Health responds 200 OK to indicate the process is alive.
func (c *HealthController) Health(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}
