package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/workflow"
	"onboarding-service/pkg/view"
)

// InternalOnboardingController serves the Auth Service's only call: recording an
// onboarding step (LLD §7). It is mounted on the internal-network route group.
type InternalOnboardingController struct {
	starter *workflow.Starter
}

// NewInternalOnboardingController returns a controller backed by the workflow starter.
func NewInternalOnboardingController(starter *workflow.Starter) *InternalOnboardingController {
	return &InternalOnboardingController{starter: starter}
}

// RegisterRoutes wires the internal routes under /v1/internal behind the guard.
func (c *InternalOnboardingController) RegisterRoutes(r gin.IRouter, guard gin.HandlerFunc) {
	g := r.Group("/v1/internal")
	g.Use(guard)
	g.POST("/onboarding/steps", c.RecordStep)
}

// RecordStep starts the user's onboarding workflow (WorkflowID = userId) on the
// first step and signals it thereafter — atomically, so it is safe under
// concurrent calls. The Auth Service calls this with EMAIL_VERIFIED.
func (c *InternalOnboardingController) RecordStep(ctx *gin.Context) {
	var req view.InternalStepRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.UserID == "" || req.StepName == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "user_id and step_name are required"})
		return
	}

	runID, err := c.starter.SubmitStep(ctx.Request.Context(), req.UserID, req.OrgID, req.StepName)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record step"})
		return
	}

	ctx.JSON(http.StatusAccepted, gin.H{
		"user_id":   req.UserID,
		"step_name": req.StepName,
		"run_id":    runID,
	})
}
