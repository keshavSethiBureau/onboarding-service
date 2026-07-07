package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/auth"
	"onboarding-service/internal/platform/authsvc"
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
	svc      stateReader
	starter  onboardingStarter
	meClient authsvc.MeClient
}

// NewOnboardingController returns a controller backed by the onboarding service,
// the workflow starter, and the Auth Service /me client (used only at signup).
func NewOnboardingController(svc stateReader, starter onboardingStarter, meClient authsvc.MeClient) *OnboardingController {
	return &OnboardingController{svc: svc, starter: starter, meClient: meClient}
}

// RegisterRoutes wires the onboarding routes under /v1/onboarding behind the
// Auth0 middleware (identity comes from the token). The middleware rejects an
// invalid/expired/missing token with 401 BEFORE any handler runs, so no handler
// below ever touches the workflow for an unauthenticated caller.
func (c *OnboardingController) RegisterRoutes(r gin.IRouter, authMW gin.HandlerFunc) {
	g := r.Group("/v1/onboarding")
	g.Use(authMW)
	g.POST("/signup", c.Signup)
	g.GET("/state", c.GetState)
	g.POST("/organisation", c.CreateOrganisation)
	g.POST("/complete", c.Complete)
}

// Signup is the SIGNUP entry point (POST /v1/onboarding/signup). Order of
// operations is chosen so a partial failure is safe to retry:
//  1. identity from the validated token (401 already enforced by middleware);
//  2. call Auth's /me BEFORE any workflow interaction — if Auth is slow/down it
//     returns a retryable 503 and NO journey is started (a retry, or the /state
//     entry, completes cleanly);
//  3. start the workflow (idempotent), signal EMAIL_VERIFIED if the token says
//     so, return the journey state.
func (c *OnboardingController) Signup(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	// Confirm the caller against Auth /me before starting anything. We forward
	// the caller's bearer token verbatim; /me logic stays in Auth.
	if _, err := c.meClient.Me(ctx.Request.Context(), ctx.GetHeader("Authorization")); err != nil {
		if errors.Is(err, authsvc.ErrAuthUnavailable) {
			// Retryable: no journey was started, so a retry is clean.
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth service unavailable, please retry", "retryable": true})
			return
		}
		// Non-retryable: Auth rejected the token (4xx).
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "auth rejected the request"})
		return
	}

	c.startAndReturnState(ctx, userID)
}

// startAndReturnState is the shared core of both entry points: start the
// per-user workflow (idempotent — one workflow per user even under concurrent
// calls), signal EMAIL_VERIFIED when the validated token carries it (a no-op if
// already recorded), then return the journey read-model state.
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

// GetState is the LOGIN/resume entry point (GET /v1/onboarding/state), called on
// every login. It starts the workflow if absent (recording the first step),
// signals EMAIL_VERIFIED when the token carries it, and returns { current_step,
// status }. Idempotent: safe to call forever — start and signal are no-ops once
// done. Unlike signup it never calls /me (nothing the journey needs is there).
func (c *OnboardingController) GetState(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.startAndReturnState(ctx, userID)
}
