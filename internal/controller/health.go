// Package controller holds the HTTP handlers and route registration.
package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// readinessTimeout bounds each dependency check behind GET /ready.
const readinessTimeout = 2 * time.Second

// ReadinessCheck is a named dependency probe for the readiness endpoint.
type ReadinessCheck struct {
	Name  string
	Probe func(context.Context) error
}

// HealthController serves the liveness and readiness probes.
//
//   - /health is liveness: 200 as long as the process is up.
//   - /ready  is readiness: 200 only when every dependency check passes, 503
//     otherwise — currently MongoDB connectivity and a non-empty vertical
//     cache (see agent_docs/onboarding-lld.md §9).
type HealthController struct {
	checks []ReadinessCheck
}

// NewHealthController returns a HealthController whose readiness probe runs the
// given checks. With no checks the service is always considered ready.
func NewHealthController(checks ...ReadinessCheck) *HealthController {
	return &HealthController{checks: checks}
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

// Ready responds 200 when all dependency checks pass, 503 otherwise, reporting
// each dependency's status.
func (c *HealthController) Ready(ctx *gin.Context) {
	results := make(map[string]string, len(c.checks))
	ready := true

	for _, check := range c.checks {
		cctx, cancel := context.WithTimeout(ctx.Request.Context(), readinessTimeout)
		err := check.Probe(cctx)
		cancel()
		if err != nil {
			results[check.Name] = "down"
			ready = false
			continue
		}
		results[check.Name] = "ok"
	}

	if !ready {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "checks": results})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"status": "ready", "checks": results})
}
