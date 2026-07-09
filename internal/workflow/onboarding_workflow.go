// Package workflow holds the Temporal workflow and activities that orchestrate
// onboarding. Temporal is the source of truth for what happened (LLD §5).
package workflow

import (
	"time"

	"go.temporal.io/sdk/activity"
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
// data a user-driven step's signal may carry. Its json tags are snake_case so the
// generic step endpoint can decode the request's opaque body.input straight into
// it without any per-step field handling.
type SignalPayload struct {
	DisplayName  string `json:"display_name"`
	VerticalName string `json:"vertical_name"`
	TncAccepted  string `json:"tnc_accepted"`
}

// Register wires the workflow and activities onto a Temporal worker. Every
// activity is registered THROUGH the shared instrumentation wrapper (under its
// original name, which the catalog dispatches by) so activity metrics live in
// one place rather than being pasted into each activity. The commons worker
// factory doesn't expose custom Temporal interceptors, so the registration
// wrapper is the one-place equivalent.
func Register(r worker.Registry, a *Activities) {
	m := a.metrics
	r.RegisterWorkflow(OnboardingWorkflow)
	r.RegisterActivityWithOptions(instrumentE(m, "PersistJourneyState", a.PersistJourneyState), activity.RegisterOptions{Name: "PersistJourneyState"})
	r.RegisterActivityWithOptions(instrumentE(m, "EmitStepEvent", a.EmitStepEvent), activity.RegisterOptions{Name: "EmitStepEvent"})
	r.RegisterActivityWithOptions(instrumentR(m, ActionCreateOrganisation, a.CreateOrganisation), activity.RegisterOptions{Name: ActionCreateOrganisation})
	r.RegisterActivityWithOptions(instrumentR(m, ActionProvisionKong, a.ProvisionKong), activity.RegisterOptions{Name: ActionProvisionKong})
	r.RegisterActivityWithOptions(instrumentR(m, ActionProvisionAWS, a.ProvisionAWS), activity.RegisterOptions{Name: ActionProvisionAWS})
	r.RegisterActivityWithOptions(instrumentR(m, ActionProvisionSvix, a.ProvisionSvix), activity.RegisterOptions{Name: ActionProvisionSvix})
	r.RegisterActivityWithOptions(instrumentR(m, ActionProvisionLago, a.ProvisionLago), activity.RegisterOptions{Name: ActionProvisionLago})
	r.RegisterActivityWithOptions(instrumentR(m, ActionCompleteProvisioning, a.CompleteProvisioning), activity.RegisterOptions{Name: ActionCompleteProvisioning})
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

	// Replay-safe logger (never a direct logger inside workflow code — that would
	// break determinism). Metrics are emitted only from activities/interceptors.
	logger := workflow.GetLogger(ctx)
	logger.Info("onboarding workflow started", "userId", in.UserID, "stepCatalogVersion", version)

	// Journey context threaded across steps (no read-model round-trips).
	orgID := ""
	displayName := ""
	tncAccepted := ""
	email := ""

	for i, step := range CatalogSteps(version) {
		// 1. Persist where the user is (drives the resume screen while awaiting a signal).
		journey.CurrentStep = step.Name
		if err := workflow.ExecuteActivity(ctx, persistActivity, journey).Get(ctx, nil); err != nil {
			logger.Error("activity failed", "action", "PersistJourneyState", "step", step.Name, "error", err)
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
				UserID: journey.UserID, OrgID: orgID, DisplayName: displayName, TncAccepted: tncAccepted, Email: email,
			}).Get(ctx, &res); err != nil {
				logger.Error("activity failed", "action", step.Action, "step", step.Name, "error", err)
				return err
			}
			if res.OrgID != "" {
				orgID = res.OrgID
				journey.OrgID = orgID
			}
			if res.Email != "" {
				email = res.Email
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
		logger.Info("step transition", "step", step.Name, "status", "completed")

		// 5. Emit analytics step-event (its own activity/retry). The funnel +
		// lifecycle counters are emitted inside EmitStepEvent; the workflow only
		// sets the flags (first step -> started; MarksComplete -> completed).
		// Timestamp is workflow.Now so it is deterministic on replay.
		_ = workflow.ExecuteActivity(ctx, emitActivity, StepEvent{
			UserID: journey.UserID, OrgID: orgID, Step: step.Name, Timestamp: now,
			WorkflowStarted: i == 0, WorkflowCompleted: step.MarksComplete,
		}).Get(ctx, nil)
	}

	// Final persist: last step completed + terminal status.
	if err := workflow.ExecuteActivity(ctx, persistActivity, journey).Get(ctx, nil); err != nil {
		logger.Error("activity failed", "action", "PersistJourneyState", "step", "final", "error", err)
		return err
	}
	logger.Info("onboarding workflow completed", "userId", in.UserID, "status", journey.Status)
	return nil
}

// Fixed (non-catalog) activity references, resolved by method name.
var (
	persistActivity = (*Activities)(nil).PersistJourneyState
	emitActivity    = (*Activities)(nil).EmitStepEvent
)
