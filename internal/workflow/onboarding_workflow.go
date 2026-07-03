// Package workflow holds the Temporal workflow and activities that orchestrate
// onboarding. Temporal is the source of truth for what happened (LLD §5).
package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"onboarding-service/internal/service/dto"
)

// TaskQueue is the Temporal task queue this service's worker polls.
const TaskQueue = "onboarding-task-queue"

// WorkflowInput starts OnboardingWorkflow. WorkflowID is the userId (one workflow
// per user, LLD §10); StepCatalogVersion pins the journey to a catalog version.
type WorkflowInput struct {
	UserID             string `json:"userId"`
	StepCatalogVersion int    `json:"stepCatalogVersion"`
}

// SignalPayload is the (typed, so replay stays deterministic — no map iteration)
// data a user-driven step's signal may carry.
type SignalPayload struct {
	DisplayName  string `json:"displayName"`
	VerticalName string `json:"verticalName"`
	TncAccepted  string `json:"tncAccepted"`
}

// Register wires the workflow and activities onto a Temporal worker. Activities
// are registered under their method names, which the catalog dispatches by.
func Register(r worker.Registry, a *Activities) {
	r.RegisterWorkflow(OnboardingWorkflow)
	r.RegisterActivity(a.PersistJourneyState)
	r.RegisterActivity(a.EmitStepEvent)
	r.RegisterActivity(a.CreateOrganisation)
	r.RegisterActivity(a.ProvisionKong)
	r.RegisterActivity(a.ProvisionAWS)
	r.RegisterActivity(a.ProvisionSvix)
	r.RegisterActivity(a.ProvisionLago)
	r.RegisterActivity(a.CompleteProvisioning)
}

// OnboardingWorkflow is the GENERIC EXECUTOR (LLD §5). It walks the pinned
// catalog version and, for each step: persists current step, waits for the
// step's signal if user-driven, then invokes the step's Action activity —
// dispatched by name from the catalog, with NO per-step branching. There is no
// manual resume logic: on crash, Temporal replay re-runs this function
// deterministically, completed activities return from history, and execution
// comes to rest exactly where it was.
func OnboardingWorkflow(ctx workflow.Context, in WorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    8,
		},
	})

	version := in.StepCatalogVersion
	if version == 0 {
		version = CatalogVersion
	}

	journey := dto.OnboardingJourney{
		UserID:             in.UserID,
		Status:             dto.StatusInProgress,
		StepCatalogVersion: version,
		StartedAt:          workflow.Now(ctx),
	}

	// Journey context threaded across steps (no read-model round-trips).
	orgID := ""
	displayName := ""
	tncAccepted := ""

	for _, step := range CatalogSteps(version) {
		// 1. Persist where the user is (drives the resume screen while awaiting a signal).
		journey.CurrentStep = step.Name
		if err := workflow.ExecuteActivity(ctx, persistActivity, journey).Get(ctx, nil); err != nil {
			return err
		}

		// 2. User-driven step: wait for its signal and absorb any payload.
		if step.Signal != "" {
			var p SignalPayload
			workflow.GetSignalChannel(ctx, step.Signal).Receive(ctx, &p)
			if p.DisplayName != "" {
				displayName = p.DisplayName
			}
			if p.VerticalName != "" {
				journey.VerticalName = p.VerticalName
			}
			if p.TncAccepted != "" {
				tncAccepted = p.TncAccepted
			}
		}

		// 3. Run the step's action (dispatched by name); merge its result.
		if step.Action != "" {
			var res ActionResult
			if err := workflow.ExecuteActivity(ctx, step.Action, ActionInput{
				UserID: journey.UserID, OrgID: orgID, DisplayName: displayName, TncAccepted: tncAccepted,
			}).Get(ctx, &res); err != nil {
				return err
			}
			if res.OrgID != "" {
				orgID = res.OrgID
				journey.OrgID = orgID
			}
		}

		// 4. Record the step completed; mark the journey completed if declared.
		now := workflow.Now(ctx)
		journey.Steps = append(journey.Steps, dto.StepSummary{
			StepName: step.Name, Status: dto.StatusCompleted, CompletedAt: &now,
		})
		if step.MarksComplete {
			journey.Status = dto.StatusCompleted
			journey.CompletedAt = &now
		}

		// 5. Emit analytics step-event (its own activity/retry).
		_ = workflow.ExecuteActivity(ctx, emitActivity, StepEvent{UserID: journey.UserID, Step: step.Name}).Get(ctx, nil)
	}

	// Final persist: last step completed + terminal status.
	return workflow.ExecuteActivity(ctx, persistActivity, journey).Get(ctx, nil)
}

// Fixed (non-catalog) activity references, resolved by method name.
var (
	persistActivity = (*Activities)(nil).PersistJourneyState
	emitActivity    = (*Activities)(nil).EmitStepEvent
)
