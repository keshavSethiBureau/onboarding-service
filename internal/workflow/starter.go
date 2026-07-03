package workflow

import (
	"context"
	"time"

	enums "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
)

// submitTimeout bounds the outbound SignalWithStart call.
const submitTimeout = 10 * time.Second

// Starter delivers per-step signals, starting the per-user workflow on the first
// one. It is the seam the endpoints call.
type Starter struct {
	client    sdkclient.Client
	taskQueue string
}

// NewStarter returns a Starter bound to a Temporal client and task queue.
func NewStarter(client sdkclient.Client, taskQueue string) *Starter {
	return &Starter{client: client, taskQueue: taskQueue}
}

// signalStep delivers a step's signal, starting the workflow if absent and
// signalling it if present — atomically, via SignalWithStartWorkflow. WorkflowID
// = userId and the start+signal is one server-side op, so concurrent calls for a
// user never create two workflows (LLD §10). Returns the workflow RunID.
func (s *Starter) signalStep(ctx context.Context, userID, signalName string, payload SignalPayload) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()

	run, err := s.client.SignalWithStartWorkflow(
		ctx,
		userID, // WorkflowID = userId
		signalName,
		payload,
		sdkclient.StartWorkflowOptions{
			ID:                       userID,
			TaskQueue:                s.taskQueue,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		},
		OnboardingWorkflow,
		WorkflowInput{UserID: userID, StepCatalogVersion: CatalogVersion},
	)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}

// SubmitStep delivers a step signal by name (the Auth Service calls this with
// EMAIL_VERIFIED). The signal name is the step name.
func (s *Starter) SubmitStep(ctx context.Context, userID, _ /*orgID*/, stepName string) (string, error) {
	return s.signalStep(ctx, userID, stepName, SignalPayload{})
}

// RequestOrganisation signals the ORGANISATION_CREATED step with the display name.
func (s *Starter) RequestOrganisation(ctx context.Context, userID, displayName string) (string, error) {
	return s.signalStep(ctx, userID, StepOrganisationCreated, SignalPayload{DisplayName: displayName})
}

// RequestComplete signals the ONBOARDING_COMPLETED step.
func (s *Starter) RequestComplete(ctx context.Context, userID string) (string, error) {
	return s.signalStep(ctx, userID, StepOnboardingCompleted, SignalPayload{})
}
