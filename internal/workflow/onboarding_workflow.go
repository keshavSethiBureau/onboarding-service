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

	// The workflow stays alive until the terminal catalog step (so a late step,
	// e.g. provisioning after ONBOARDING_COMPLETED, reaches THIS run and never
	// spawns a second workflow). journey.Status flips to completed earlier, on
	// ONBOARDING_COMPLETED, so the user reaches the homepage without waiting.
	ch := workflow.GetSignalChannel(ctx, StepSignalName)
	for !isCompleted(&journey, TerminalStep) {
		var sig StepSignal
		ch.Receive(ctx, &sig)

		// Ignore duplicate / out-of-order / unknown steps: only persist on a
		// real state change, so Auth Service retries are effect-idempotent.
		if !applyStep(&journey, sig, workflow.Now(ctx)) {
			continue
		}
		if err := workflow.ExecuteActivity(ctx, persistActivityName, journey).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// persistActivityName references PersistJourneyState by name for ExecuteActivity;
// a nil receiver is fine because it is only used for name resolution, never called.
var persistActivityName = (*Activities)(nil).PersistJourneyState

// catalogIndex maps a step name to its catalog position for progress ordering.
// Built once at init; only point lookups are done in the workflow (deterministic).
var catalogIndex = func() map[string]int {
	m := make(map[string]int, len(Catalog))
	for i, s := range Catalog {
		m[s] = i
	}
	return m
}()

// applyStep records a signalled step as completed and recomputes currentStep as
// the furthest-progressed step. It returns false (no change) when the step is
// unknown or already completed, so callers can skip persisting. currentStep
// never regresses on a duplicate/out-of-order signal.
func applyStep(j *dto.OnboardingJourney, sig StepSignal, now time.Time) bool {
	if _, known := catalogIndex[sig.StepName]; !known {
		return false // not a catalog step — ignore
	}
	if isCompleted(j, sig.StepName) {
		if sig.OrgID != "" && j.OrgID == "" {
			j.OrgID = sig.OrgID
		}
		return false // already done — idempotent no-op
	}
	if sig.OrgID != "" {
		j.OrgID = sig.OrgID
	}

	completedAt := now
	j.Steps = append(j.Steps, dto.StepSummary{
		StepName:    sig.StepName,
		Status:      dto.StatusCompleted,
		CompletedAt: &completedAt,
	})

	j.CurrentStep = furthestCompletedStep(j)
	j.UpdatedAt = now
	if isCompleted(j, StepOnboardingCompleted) && j.CompletedAt == nil {
		j.Status = dto.StatusCompleted
		j.CompletedAt = &completedAt
	}
	return true
}

// isCompleted reports whether a step is present and completed in the summary.
func isCompleted(j *dto.OnboardingJourney, stepName string) bool {
	for i := range j.Steps {
		if j.Steps[i].StepName == stepName {
			return j.Steps[i].Status == dto.StatusCompleted
		}
	}
	return false
}

// furthestCompletedStep returns the completed step with the highest catalog
// index — the user's true progress, immune to out-of-order signals.
func furthestCompletedStep(j *dto.OnboardingJourney) string {
	best, bestIdx := "", -1
	for i := range j.Steps {
		if j.Steps[i].Status != dto.StatusCompleted {
			continue
		}
		if idx := catalogIndex[j.Steps[i].StepName]; idx > bestIdx {
			best, bestIdx = j.Steps[i].StepName, idx
		}
	}
	return best
}
