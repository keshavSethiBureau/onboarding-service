package workflow

import (
	"context"

	sdkclient "go.temporal.io/sdk/client"
)

// Starter submits onboarding step signals, starting the per-user workflow on the
// first one. It is the seam the internal steps endpoint calls.
type Starter struct {
	client    sdkclient.Client
	taskQueue string
}

// NewStarter returns a Starter bound to a Temporal client and task queue.
func NewStarter(client sdkclient.Client, taskQueue string) *Starter {
	return &Starter{client: client, taskQueue: taskQueue}
}

// SubmitStep delivers a step to the user's workflow, starting it if absent and
// signalling it if present — atomically, via Temporal's SignalWithStartWorkflow.
// Because WorkflowID = userId and the start+signal is a single server-side
// operation, concurrent calls for the same user can never create two workflows
// (LLD §10): the first wins the start, the rest signal the same run. Returns the
// RunID of the (possibly pre-existing) workflow.
func (s *Starter) SubmitStep(ctx context.Context, userID, orgID, stepName string) (string, error) {
	run, err := s.client.SignalWithStartWorkflow(
		ctx,
		userID, // WorkflowID = userId — the one-workflow-per-user key
		StepSignalName,
		StepSignal{StepName: stepName, OrgID: orgID},
		sdkclient.StartWorkflowOptions{ID: userID, TaskQueue: s.taskQueue},
		OnboardingWorkflow,
		WorkflowInput{UserID: userID, OrgID: orgID},
	)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}
