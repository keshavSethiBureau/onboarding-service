package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"onboarding-service/internal/platform/provisioning"
	"onboarding-service/internal/repo"
)

// failingProvisioner fails Svix/Lago on every attempt (Kong/AWS still succeed),
// simulating an end-of-onboarding provisioning outage.
type failingProvisioner struct{ countingProvisioner }

func (f *failingProvisioner) Svix(context.Context, provisioning.ProvisionInput) (string, error) {
	return "", errors.New("svix down")
}
func (f *failingProvisioner) Lago(context.Context, provisioning.ProvisionInput) (string, error) {
	return "", errors.New("lago down")
}

// snapshotRepo persists like captureRepo but also snapshots how many Svix/Lago
// provisioning calls had happened at the moment of each upsert, so tests can
// prove ordering (completed-status persisted BEFORE provisioning ran).
type snapshotRepo struct {
	prov  *countingProvisioner
	snaps []journeySnap
}

type journeySnap struct {
	step      string
	status    string
	svixCalls int
	lagoCalls int
}

func (s *snapshotRepo) Upsert(_ context.Context, doc *repo.OnboardingJourneyDoc) error {
	s.snaps = append(s.snaps, journeySnap{
		step: doc.CurrentStep, status: string(doc.Status),
		svixCalls: s.prov.svix, lagoCalls: s.prov.lago,
	})
	return nil
}
func (s *snapshotRepo) FindByUserID(context.Context, string) (*repo.OnboardingJourneyDoc, error) {
	return nil, nil
}
func (s *snapshotRepo) SetOrgID(context.Context, string, string) error { return nil }

// driveTestEnvSignals schedules every user-driven signal of the v1 walk.
func driveTestEnvSignals(env *testsuite.TestWorkflowEnvironment) {
	for i, step := range CatalogSteps(CatalogVersion) {
		if step.Signal == "" {
			continue
		}
		s := step.Signal
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(s, SignalPayload{DisplayName: "Acme"})
		}, time.Duration(i+1)*time.Millisecond)
	}
}

// TestComplete_ReturnsBeforeProvisioningFinishes proves the user-facing
// completion is not gated on end-of-onboarding provisioning: the journey is
// persisted with status=completed BEFORE the Svix/Lago provisioners have run
// even once (the /complete handler returns 202 at signal delivery — this pins
// the workflow-side ordering that makes that safe).
func TestComplete_ReturnsBeforeProvisioningFinishes(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	prov := &countingProvisioner{}
	journeys := &snapshotRepo{prov: prov}
	Register(env, NewActivities(journeys, newFakeProvisioningRepo(), &countingOrgCreator{}, prov))
	driveTestEnvSignals(env)

	env.ExecuteWorkflow(OnboardingWorkflow, WorkflowInput{UserID: "u1", StepCatalogVersion: CatalogVersion})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow did not complete cleanly: %v", env.GetWorkflowError())
	}

	// Find the first persist that showed the user a completed journey.
	first := -1
	for i, s := range journeys.snaps {
		if s.status == "completed" {
			first = i
			break
		}
	}
	if first == -1 {
		t.Fatal("journey was never persisted as completed")
	}
	got := journeys.snaps[first]
	if got.svixCalls != 0 || got.lagoCalls != 0 {
		t.Errorf("completed was persisted AFTER provisioning ran (svix=%d lago=%d calls) — user was blocked on provisioning",
			got.svixCalls, got.lagoCalls)
	}
	// And provisioning did eventually run (the walk finished the tail steps).
	if prov.svix != 1 || prov.lago != 1 {
		t.Errorf("provisioning after completion: svix=%d lago=%d, want 1 each", prov.svix, prov.lago)
	}
}

// TestComplete_ProvisioningFailureDoesNotBlockUser proves the failure path:
// Svix/Lago failing on every retry leaves the workflow erroring in the
// background, but the journey read-model was already persisted completed — the
// user proceeded to the homepage and is NOT blocked.
func TestComplete_ProvisioningFailureDoesNotBlockUser(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	prov := &failingProvisioner{}
	journeys := &snapshotRepo{prov: &prov.countingProvisioner}
	provRecords := newFakeProvisioningRepo()
	Register(env, NewActivities(journeys, provRecords, &countingOrgCreator{}, prov))
	driveTestEnvSignals(env)

	env.ExecuteWorkflow(OnboardingWorkflow, WorkflowInput{UserID: "u1", StepCatalogVersion: CatalogVersion})

	// The workflow itself fails once PROVISION_SVIX exhausts its retries...
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow error after provisioning retries exhausted")
	}

	// ...but the user already saw a completed journey.
	completed := false
	for _, s := range journeys.snaps {
		if s.status == "completed" {
			completed = true
			break
		}
	}
	if !completed {
		t.Error("journey was never persisted completed — provisioning failure blocked the user")
	}

	// And the provisioning record was never marked completed (no false success).
	rec, err := provRecords.GetByOrgID(context.Background(), "org_u1")
	if err != nil {
		t.Fatalf("GetByOrgID: %v", err)
	}
	if rec != nil && rec.Status == repo.ProvisioningStatusCompleted {
		t.Errorf("provisioning record marked completed despite Svix/Lago failure: %+v", rec)
	}
}
