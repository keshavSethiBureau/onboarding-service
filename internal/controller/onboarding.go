package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bureau/onboarding-service/internal/auth"
	"github.com/bureau/onboarding-service/internal/service/impl"
	"github.com/bureau/onboarding-service/pkg/view"
)

// OnboardingController serves the authenticated onboarding endpoints.
type OnboardingController struct {
	svc *impl.OnboardingService
}

// NewOnboardingController returns a controller backed by the onboarding service.
func NewOnboardingController(svc *impl.OnboardingService) *OnboardingController {
	return &OnboardingController{svc: svc}
}

// RegisterRoutes wires the onboarding routes under /v1/onboarding behind the
// Auth0 middleware (identity comes from the token).
func (c *OnboardingController) RegisterRoutes(r gin.IRouter, authMW gin.HandlerFunc) {
	g := r.Group("/v1/onboarding")
	g.Use(authMW)
	g.GET("/state", c.GetState)
}

// GetState returns { current_step, status } for the authenticated user.
func (c *OnboardingController) GetState(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	journey, err := c.svc.GetState(ctx.Request.Context(), userID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load onboarding state"})
		return
	}

	ctx.JSON(http.StatusOK, view.OnboardingStateResponse{
		CurrentStep: journey.CurrentStep,
		Status:      journey.Status,
	})
}
