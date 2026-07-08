package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"onboarding-service/internal/observability"
	"onboarding-service/internal/workflow"
	"onboarding-service/pkg/view"
)

// InternalOnboardingController serves the Auth Service's only call: recording an
// onboarding step (LLD §7). It is mounted on the internal-network route group.
type InternalOnboardingController struct {
	starter *workflow.Starter
	metrics *observability.Metrics // nil-safe
}

// NewInternalOnboardingController returns a controller backed by the workflow
// starter. metrics may be nil (metrics then skipped).
func NewInternalOnboardingController(starter *workflow.Starter, metrics *observability.Metrics) *InternalOnboardingController {
	return &InternalOnboardingController{starter: starter, metrics: metrics}
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
		c.recordMetric("unknown", observability.StatusError)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.UserID == "" || req.StepName == "" {
		c.recordMetric(workflow.StepLabel(req.StepName), observability.StatusError)
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "user_id and step_name are required"})
		return
	}

	runID, err := c.starter.SubmitStep(ctx.Request.Context(), req.UserID, req.OrgID, req.StepName)
	if err != nil {
		c.recordMetric(workflow.StepLabel(req.StepName), observability.StatusError)
		observability.Log(ctx.Request.Context()).Error("internal step call failed",
			"step", workflow.StepLabel(req.StepName), "userId", req.UserID, "error", err.Error())
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record step"})
		return
	}

	c.recordMetric(workflow.StepLabel(req.StepName), observability.StatusSuccess)
	ctx.JSON(http.StatusAccepted, gin.H{
		"user_id":   req.UserID,
		"step_name": req.StepName,
		"run_id":    runID,
	})
}

// recordMetric increments the Auth-handoff counter (step already bucketed).
func (c *InternalOnboardingController) recordMetric(step, status string) {
	if c.metrics != nil {
		c.metrics.InternalStepsReceived.WithLabelValues(step, status).Inc()
	}
}
