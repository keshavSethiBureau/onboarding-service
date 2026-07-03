package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/config"
)

// VerticalsController serves the public verticals listing from the in-memory
// cache (LLD §6). It never touches Mongo — verticals live in Apollo config.
type VerticalsController struct {
	cache *config.VerticalCache
}

// NewVerticalsController returns a controller backed by the vertical cache.
func NewVerticalsController(cache *config.VerticalCache) *VerticalsController {
	return &VerticalsController{cache: cache}
}

// RegisterRoutes wires the public verticals routes under /v1.
func (c *VerticalsController) RegisterRoutes(r gin.IRouter) {
	r.GET("/v1/verticals", c.List)
}

// List returns the active verticals from the cache.
func (c *VerticalsController) List(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"verticals": c.cache.Verticals()})
}
