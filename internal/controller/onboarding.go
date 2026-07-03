package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/auth"
	"onboarding-service/internal/service/impl"
	"onboarding-service/internal/workflow"
	"onboarding-service/pkg/view"
)

// OnboardingController serves the authenticated onboarding endpoints.
type OnboardingController struct {
	svc     *impl.OnboardingService
	starter *workflow.Starter
}

// NewOnboardingController returns a controller backed by the onboarding service
// and the workflow starter.
func NewOnboardingController(svc *impl.OnboardingService, starter *workflow.Starter) *OnboardingController {
	return &OnboardingController{svc: svc, starter: starter}
}

// RegisterRoutes wires the onboarding routes under /v1/onboarding behind the
// Auth0 middleware (identity comes from the token).
func (c *OnboardingController) RegisterRoutes(r gin.IRouter, authMW gin.HandlerFunc) {
	g := r.Group("/v1/onboarding")
	g.Use(authMW)
	g.GET("/state", c.GetState)
	g.POST("/organisation", c.CreateOrganisation)
	g.POST("/complete", c.Complete)
}

// Complete signals the workflow to finish onboarding. It returns 202 immediately
// (before end-of-onboarding provisioning runs): the workflow marks the journey
// completed so the user proceeds to the homepage, then provisions Svix + Lago
// independently — a provisioning failure never blocks this response.
func (c *OnboardingController) Complete(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	runID, err := c.starter.RequestComplete(ctx.Request.Context(), userID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to complete onboarding"})
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"user_id": userID, "run_id": runID})
}

// CreateOrganisation triggers organisation creation for the authenticated user.
// Go calls Auth0 (via the workflow's CreateOrganisation activity), records
// ORGANISATION_CREATED, and runs the migrated post-org setup. The workflow is
// started if absent (WorkflowID = userId). Accepted (202); the resulting orgId
// lands on the journey read-model (GET /v1/onboarding/state).
func (c *OnboardingController) CreateOrganisation(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req view.CreateOrganisationRequest
	if err := ctx.ShouldBindJSON(&req); err != nil || req.DisplayName == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "display_name is required"})
		return
	}
	if req.TncAccepted == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "tnc_accepted is required"})
		return
	}

	runID, err := c.starter.RequestOrganisation(ctx.Request.Context(), userID, req.DisplayName, req.TncAccepted)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to request organisation creation"})
		return
	}

	ctx.JSON(http.StatusAccepted, gin.H{"user_id": userID, "run_id": runID})
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
