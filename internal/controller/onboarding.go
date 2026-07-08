package controller

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/auth"
	// REMOVED(single-entry): "errors" + Auth /me client — signup and the only
	// Auth-service call were removed; GET /v1/onboarding/state is the sole entry.
	// "errors"
	// "onboarding-service/internal/platform/authsvc"
	"onboarding-service/internal/service/dto"
	"onboarding-service/pkg/view"
)

// onboardingStarter is the workflow-start seam the controller needs (satisfied
// by *workflow.Starter). Defined here so handlers are testable without Temporal.
type onboardingStarter interface {
	Start(ctx context.Context, userID string) (string, error)
	SignalEmailVerified(ctx context.Context, userID string) error
	RequestOrganisation(ctx context.Context, userID, displayName, tncAccepted string) (string, error)
	RequestComplete(ctx context.Context, userID string) (string, error)
}

// stateReader reads the journey read-model (satisfied by *impl.OnboardingService).
type stateReader interface {
	GetState(ctx context.Context, userID string) (*dto.OnboardingJourney, error)
}

// OnboardingController serves the authenticated onboarding endpoints.
type OnboardingController struct {
	svc     stateReader
	starter onboardingStarter
	// REMOVED(single-entry): meClient authsvc.MeClient — this service makes no
	// calls to the Auth Service; /me is never called.
}

// NewOnboardingController returns a controller backed by the onboarding service
// and the workflow starter.
func NewOnboardingController(svc stateReader, starter onboardingStarter) *OnboardingController {
	return &OnboardingController{svc: svc, starter: starter}
}

// RegisterRoutes wires the onboarding routes under /v1/onboarding behind the
// Auth0 middleware (identity comes from the token). The middleware rejects an
// invalid/expired/missing token with 401 BEFORE any handler runs, so no handler
// below ever touches the workflow for an unauthenticated caller.
func (c *OnboardingController) RegisterRoutes(r gin.IRouter, authMW gin.HandlerFunc) {
	g := r.Group("/v1/onboarding")
	g.Use(authMW)
	// REMOVED(single-entry): g.POST("/signup", c.Signup) — GET /state is the sole
	// journey entry point (create-if-absent); there is no signup endpoint.
	g.GET("/state", c.GetState)
	g.POST("/organisation", c.CreateOrganisation)
	g.POST("/complete", c.Complete)
}

// REMOVED(single-entry): the POST /v1/onboarding/signup handler and its Auth /me
// call are gone. GET /v1/onboarding/state is the single journey entry point
// (create-if-absent), and this service never calls the Auth Service. Kept
// commented per the removal convention.
//
// func (c *OnboardingController) Signup(ctx *gin.Context) {
// 	userID, _, ok := auth.Identity(ctx)
// 	if !ok {
// 		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
// 		return
// 	}
// 	// Confirm the caller against Auth /me before starting anything.
// 	if _, err := c.meClient.Me(ctx.Request.Context(), ctx.GetHeader("Authorization")); err != nil {
// 		if errors.Is(err, authsvc.ErrAuthUnavailable) {
// 			ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth service unavailable, please retry", "retryable": true})
// 			return
// 		}
// 		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "auth rejected the request"})
// 		return
// 	}
// 	c.startAndReturnState(ctx, userID)
// }

// startAndReturnState is the core of the single journey entry point (GET
// /v1/onboarding/state): start the per-user workflow if absent (idempotent — one
// workflow per user even under concurrent calls; records USER_SIGNED_UP), signal
// EMAIL_VERIFIED when the validated token carries it (a no-op if already
// recorded), then return the journey read-model state.
func (c *OnboardingController) startAndReturnState(ctx *gin.Context, userID string) {
	if _, err := c.starter.Start(ctx.Request.Context(), userID); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start onboarding"})
		return
	}
	if auth.EmailVerified(ctx) {
		if err := c.starter.SignalEmailVerified(ctx.Request.Context(), userID); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record email verification"})
			return
		}
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

// GetState is the SINGLE journey entry point (GET /v1/onboarding/state), called
// on signup and on every login. If no journey exists for the token's userId it
// creates one (starts the workflow, records USER_SIGNED_UP); it signals
// EMAIL_VERIFIED when the token's email_verified claim is true, and returns
// { current_step, status }. Idempotent: safe to call forever — create-if-absent
// and duplicate step signals are no-ops; a completed journey just returns state.
// This service never calls the Auth Service.
func (c *OnboardingController) GetState(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.startAndReturnState(ctx, userID)
}
