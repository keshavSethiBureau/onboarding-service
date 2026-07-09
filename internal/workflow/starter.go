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
		// Pin the latest catalog version at start. USE_EXISTING means this input
		// is honoured only when the workflow is first created, so an in-flight
		// journey keeps its original pinned version.
		WorkflowInput{UserID: userID, StepCatalogVersion: LatestCatalogVersion()},
	)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}

// Start starts the per-user workflow WITHOUT sending a signal, recording the
// first catalog step. It is idempotent: WorkflowID = userId and the conflict
// policy is USE_EXISTING, so a start for a user whose workflow already exists is
// a no-op that returns the existing run. This is the entry-point primitive for
// /v1/onboarding/signup and /v1/onboarding/state — two concurrent calls for the
// same user therefore yield exactly ONE workflow. Returns the workflow RunID.
func (s *Starter) Start(ctx context.Context, userID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()

	run, err := s.client.ExecuteWorkflow(
		ctx,
		sdkclient.StartWorkflowOptions{
			ID:                       userID, // WorkflowID = userId
			TaskQueue:                s.taskQueue,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		},
		OnboardingWorkflow,
		// Pinned only when the workflow is first created; USE_EXISTING keeps an
		// in-flight journey on its original catalog version.
		WorkflowInput{UserID: userID, StepCatalogVersion: LatestCatalogVersion()},
	)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}

// SignalEmailVerified signals the EMAIL_VERIFIED step. Signalling an
// already-completed step is a no-op on the workflow side (the executor has moved
// past it), so this is safe to call on every login. Assumes the workflow exists
// (call Start first).
func (s *Starter) SignalEmailVerified(ctx context.Context, userID string) error {
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	return s.client.SignalWorkflow(ctx, userID, "", StepEmailVerified, SignalPayload{})
}

// SubmitStep delivers a step signal by name (the Auth Service calls this with
// EMAIL_VERIFIED). The signal name is the step name.
func (s *Starter) SubmitStep(ctx context.Context, userID, _ /*orgID*/, stepName string) (string, error) {
	return s.signalStep(ctx, userID, stepName, SignalPayload{})
}

// SignalStep delivers a user-input step's signal (with its payload) to an
// EXISTING workflow — no start. This is the primitive the generic step endpoint
// (POST /v1/onboarding/steps/{step_name}) uses: the journey already exists
// (created by GET /v1/onboarding/state), so we only signal. A duplicate signal
// for a step the workflow has already passed is ignored by Temporal (idempotent).
func (s *Starter) SignalStep(ctx context.Context, userID, stepName string, payload SignalPayload) error {
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	return s.client.SignalWorkflow(ctx, userID, "", stepName, payload)
}

// RETIRED(generic-steps): the typed RequestOrganisation / RequestComplete signal
// helpers are retired — organisation creation and completion now advance their
// catalog steps (ORGANISATION_CREATED, ONBOARDING_COMPLETED) via SignalStep from
// the generic endpoint. Retained commented per the removal convention.
//
// // RequestOrganisation signals the ORGANISATION_CREATED step with the display name
// // and the user's T&C acceptance.
// func (s *Starter) RequestOrganisation(ctx context.Context, userID, displayName, tncAccepted string) (string, error) {
// 	return s.signalStep(ctx, userID, StepOrganisationCreated, SignalPayload{DisplayName: displayName, TncAccepted: tncAccepted})
// }
//
// // RequestComplete signals the ONBOARDING_COMPLETED step.
// func (s *Starter) RequestComplete(ctx context.Context, userID string) (string, error) {
// 	return s.signalStep(ctx, userID, StepOnboardingCompleted, SignalPayload{})
// }
