// Package workflow holds the Temporal workflow and activities that orchestrate
// onboarding. Temporal is the source of truth for what happened (LLD §5).
//
// For now this is a trivial skeleton: a no-op OnboardingWorkflow with one no-op
// activity, enough to prove the worker runs and a workflow can start. The real
// signal-driven step orchestration and provisioning activities come later.
package workflow

import (
	"context"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the Temporal task queue this service's worker polls.
const TaskQueue = "onboarding-task-queue"

// Register wires the workflow and activities onto a Temporal worker.
func Register(r worker.Registry) {
	r.RegisterWorkflow(OnboardingWorkflow)
	r.RegisterActivity(NoOp)
}

// OnboardingWorkflow is one workflow per user (WorkflowID = userId, LLD §5).
// It currently just invokes a no-op activity and returns — a placeholder for
// the step/signal orchestration to be built next.
func OnboardingWorkflow(ctx workflow.Context, userID string) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})
	return workflow.ExecuteActivity(ctx, NoOp).Get(ctx, nil)
}

// NoOp is a placeholder activity that does nothing and always succeeds.
func NoOp(ctx context.Context) error {
	return nil
}
