package controller

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/auth"
	// REMOVED(single-entry): "errors" + Auth /me client — signup and the only
	// Auth-service call were removed; GET /v1/onboarding/state is the sole entry.
	// "errors"
	// "onboarding-service/internal/platform/authsvc"
	"onboarding-service/internal/service/dto"
	"onboarding-service/internal/workflow"
	"onboarding-service/pkg/view"
)

// onboardingStarter is the workflow seam the controller needs (satisfied by
// *workflow.Starter). Defined here so handlers are testable without Temporal.
type onboardingStarter interface {
	Start(ctx context.Context, userID string) (string, error)
	SignalEmailVerified(ctx context.Context, userID string) error
	// SignalStep advances a user-input step on an existing workflow (generic path).
	SignalStep(ctx context.Context, userID, stepName string, payload workflow.SignalPayload) error
}

// stateReader reads the journey read-model (satisfied by *impl.OnboardingService).
type stateReader interface {
	GetState(ctx context.Context, userID string) (*dto.OnboardingJourney, error)
}

// OnboardingController serves the authenticated onboarding endpoints.
type OnboardingController struct {
	svc        stateReader
	starter    onboardingStarter
	validators ValidatorRegistry // per-step input validators for the generic endpoint
	// REMOVED(single-entry): meClient authsvc.MeClient — this service makes no
	// calls to the Auth Service; /me is never called.
}

// NewOnboardingController returns a controller backed by the onboarding service,
// the workflow starter, and the per-step validator registry.
func NewOnboardingController(svc stateReader, starter onboardingStarter, validators ValidatorRegistry) *OnboardingController {
	return &OnboardingController{svc: svc, starter: starter, validators: validators}
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
	// The single write path for user-input steps (catalog-driven, generic).
	g.POST("/steps/:step_name", c.AdvanceStep)
	// RETIRED(generic-steps): the typed org-creation + completion endpoints are
	// replaced by advancing their catalog steps via POST /steps/:step_name.
	// g.POST("/organisation", c.CreateOrganisation)
	// g.POST("/complete", c.Complete)
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

	c.writeState(ctx, userID)
}

// writeState reads the journey read-model and writes { current_step, status }
// (200). Shared by the state entry point and the generic step endpoint.
func (c *OnboardingController) writeState(ctx *gin.Context, userID string) {
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

// AdvanceStep is the SINGLE write path for user-input steps
// (POST /v1/onboarding/steps/:step_name). It is generic and catalog-driven — the
// same handler serves every user-advanceable step, with per-step input rules in
// the validator registry and the ordering guard in exactly one place here.
func (c *OnboardingController) AdvanceStep(ctx *gin.Context) {
	userID, _, ok := auth.Identity(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	stepName := ctx.Param("step_name")

	journey, err := c.svc.GetState(ctx.Request.Context(), userID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load onboarding state"})
		return
	}

	// Idempotency: a completed journey, or a step already completed, is a no-op
	// that just returns current state.
	if journey.Status == dto.StatusCompleted || stepCompleted(journey, stepName) {
		c.writeState(ctx, userID)
		return
	}

	// Ordering guard (ONE place): the step must be in the caller's pinned catalog
	// version AND be their current step; otherwise it is out of order.
	if !workflow.StepInCatalog(journey.StepCatalogVersion, stepName) || stepName != journey.CurrentStep {
		ctx.JSON(http.StatusConflict, gin.H{"error": "step not current"})
		return
	}

	// Parse the opaque per-step input; validate it via the registry (skipped when
	// the step has no validator).
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil && err.Error() != "EOF" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := c.validators.Validate(stepName, body.Input); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Decode the (already-validated) opaque input into the typed signal payload
	// with no per-step field handling, then signal the step's channel.
	var payload workflow.SignalPayload
	if len(body.Input) > 0 {
		if err := json.Unmarshal(body.Input, &payload); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid input"})
			return
		}
	}
	if err := c.starter.SignalStep(ctx.Request.Context(), userID, stepName, payload); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to advance step"})
		return
	}

	c.writeState(ctx, userID)
}

// stepCompleted reports whether the journey has already recorded stepName as
// completed (used for idempotent re-submits).
func stepCompleted(journey *dto.OnboardingJourney, stepName string) bool {
	for _, s := range journey.Steps {
		if s.StepName == stepName && s.Status == dto.StatusCompleted {
			return true
		}
	}
	return false
}

// RETIRED(generic-steps): the typed Complete + CreateOrganisation handlers are
// replaced by advancing ONBOARDING_COMPLETED / ORGANISATION_CREATED through the
// generic AdvanceStep path. Their validation now lives in the validator registry
// (see stepvalidator.go) and their side effects in the step activities. Retained
// commented per the removal convention.
//
// func (c *OnboardingController) Complete(ctx *gin.Context) {
// 	userID, _, ok := auth.Identity(ctx)
// 	if !ok {
// 		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
// 		return
// 	}
// 	runID, err := c.starter.RequestComplete(ctx.Request.Context(), userID)
// 	if err != nil {
// 		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to complete onboarding"})
// 		return
// 	}
// 	ctx.JSON(http.StatusAccepted, gin.H{"user_id": userID, "run_id": runID})
// }
//
// func (c *OnboardingController) CreateOrganisation(ctx *gin.Context) {
// 	userID, _, ok := auth.Identity(ctx)
// 	if !ok {
// 		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
// 		return
// 	}
// 	var req view.CreateOrganisationRequest
// 	if err := ctx.ShouldBindJSON(&req); err != nil || req.DisplayName == "" {
// 		ctx.JSON(http.StatusBadRequest, gin.H{"error": "display_name is required"})
// 		return
// 	}
// 	if req.TncAccepted == "" {
// 		ctx.JSON(http.StatusBadRequest, gin.H{"error": "tnc_accepted is required"})
// 		return
// 	}
// 	runID, err := c.starter.RequestOrganisation(ctx.Request.Context(), userID, req.DisplayName, req.TncAccepted)
// 	if err != nil {
// 		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to request organisation creation"})
// 		return
// 	}
// 	ctx.JSON(http.StatusAccepted, gin.H{"user_id": userID, "run_id": runID})
// }

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
