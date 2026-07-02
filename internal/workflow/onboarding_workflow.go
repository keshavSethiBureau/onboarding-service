// Package workflow holds the Temporal workflow and activities that orchestrate
// onboarding. Temporal is the source of truth for what happened (LLD §5).
package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/bureau/onboarding-service/internal/service/dto"
)

// TaskQueue is the Temporal task queue this service's worker polls.
const TaskQueue = "onboarding-task-queue"

// StepSignalName is the signal channel that advances a journey. Every step
// transition (from the Auth Service or the frontend) is delivered here.
const StepSignalName = "onboarding-step"

// WorkflowInput is the start argument for OnboardingWorkflow. WorkflowID is the
// userId, so the workflow is one-per-user (LLD §10).
type WorkflowInput struct {
	UserID string `json:"userId"`
	OrgID  string `json:"orgId"`
}

// StepSignal advances the journey to a step (a completed onboarding action).
type StepSignal struct {
	StepName string `json:"stepName"`
	OrgID    string `json:"orgId"`
}

// Register wires the workflow and activities onto a Temporal worker.
func Register(r worker.Registry, a *Activities) {
	r.RegisterWorkflow(OnboardingWorkflow)
	r.RegisterActivity(a.PersistJourneyState)
}

// OnboardingWorkflow is one workflow per user (WorkflowID = userId, LLD §5). It
// pins the step-catalog version on start, then advances on StepSignal signals,
// persisting the denormalised read-model (currentStep + embedded step summary)
// on every transition. It completes when ONBOARDING_COMPLETED is reached.
func OnboardingWorkflow(ctx workflow.Context, in WorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 5},
	})

	// Initial state; stepCatalogVersion is pinned here for the workflow's life.
	journey := dto.OnboardingJourney{
		UserID:             in.UserID,
		OrgID:              in.OrgID,
		Status:             dto.StatusInProgress,
		StepCatalogVersion: CatalogVersion,
		StartedAt:          workflow.Now(ctx),
	}

	ch := workflow.GetSignalChannel(ctx, StepSignalName)
	for journey.Status != dto.StatusCompleted {
		var sig StepSignal
		ch.Receive(ctx, &sig)

		applyStep(&journey, sig, workflow.Now(ctx))

		if err := workflow.ExecuteActivity(ctx, persistActivityName, journey).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// persistActivityName references PersistJourneyState by name for ExecuteActivity;
// a nil receiver is fine because it is only used for name resolution, never called.
var persistActivityName = (*Activities)(nil).PersistJourneyState

// applyStep records a signalled step as completed, updates currentStep, and
// marks the journey completed when the terminal step is reached.
func applyStep(j *dto.OnboardingJourney, sig StepSignal, now time.Time) {
	if sig.OrgID != "" {
		j.OrgID = sig.OrgID
	}

	completedAt := now
	updated := false
	for i := range j.Steps {
		if j.Steps[i].StepName == sig.StepName {
			j.Steps[i].Status = dto.StatusCompleted
			j.Steps[i].CompletedAt = &completedAt
			updated = true
			break
		}
	}
	if !updated {
		j.Steps = append(j.Steps, dto.StepSummary{
			StepName:    sig.StepName,
			Status:      dto.StatusCompleted,
			CompletedAt: &completedAt,
		})
	}

	j.CurrentStep = sig.StepName
	j.UpdatedAt = now
	if sig.StepName == StepOnboardingCompleted {
		j.Status = dto.StatusCompleted
		j.CompletedAt = &completedAt
	}
}
