package workflow

import (
	"context"
	"time"

	enums "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
)

// submitTimeout bounds the outbound SignalWithStart call so a slow Temporal
// frontend can't pin the caller's request indefinitely.
const submitTimeout = 10 * time.Second

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
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()

	run, err := s.client.SignalWithStartWorkflow(
		ctx,
		userID, // WorkflowID = userId — the one-workflow-per-user key
		StepSignalName,
		StepSignal{StepName: stepName, OrgID: orgID},
		sdkclient.StartWorkflowOptions{
			ID:        userID,
			TaskQueue: s.taskQueue,
			// USE_EXISTING pins the concurrency guarantee in code: if a workflow
			// with this ID is already running, signal it rather than starting a
			// second one. Not left to the server default.
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			// WorkflowIDReusePolicy is intentionally left at the default
			// (AllowDuplicate): re-onboarding after a fully-completed+terminated
			// journey is a product decision for the provisioning module (LLD §5),
			// not settled here.
		},
		OnboardingWorkflow,
		WorkflowInput{UserID: userID, OrgID: orgID},
	)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}
