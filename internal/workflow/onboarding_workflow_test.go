package workflow

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/bureau/onboarding-service/internal/repo"
	"github.com/bureau/onboarding-service/internal/service/dto"
)

// captureRepo records every upserted doc so the test can inspect the sequence of
// journey states the workflow persisted.
type captureRepo struct {
	docs []*repo.OnboardingJourneyDoc
}

func (c *captureRepo) Upsert(_ context.Context, doc *repo.OnboardingJourneyDoc) error {
	// copy: the workflow reuses the same value across transitions
	cp := *doc
	c.docs = append(c.docs, &cp)
	return nil
}

func (c *captureRepo) FindByUserID(context.Context, string) (*repo.OnboardingJourneyDoc, error) {
	return nil, nil
}

func TestOnboardingWorkflow_AdvancesAndPersistsPerTransition(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	capture := &captureRepo{}
	env.RegisterActivity(NewActivities(capture).PersistJourneyState)

	// Deliver EMAIL_VERIFIED (start), then a mid step, then the terminal step.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepSignalName, StepSignal{StepName: StepEmailVerified, OrgID: "org-1"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepSignalName, StepSignal{StepName: StepVerticalSelected})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepSignalName, StepSignal{StepName: StepOnboardingCompleted})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(OnboardingWorkflow, WorkflowInput{UserID: "user-1", OrgID: "org-1"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	// One persist per transition (3 signals).
	if len(capture.docs) != 3 {
		t.Fatalf("expected 3 persisted transitions, got %d", len(capture.docs))
	}

	// catalog version pinned on every persist; orgId propagated from the signal.
	for i, d := range capture.docs {
		if d.StepCatalogVersion != CatalogVersion {
			t.Errorf("doc[%d] catalog version = %d, want %d", i, d.StepCatalogVersion, CatalogVersion)
		}
		if d.UserID != "user-1" || d.OrgID != "org-1" {
			t.Errorf("doc[%d] identity = %s/%s", i, d.UserID, d.OrgID)
		}
	}

	// currentStep advances with each transition.
	wantSteps := []string{StepEmailVerified, StepVerticalSelected, StepOnboardingCompleted}
	for i, want := range wantSteps {
		if capture.docs[i].CurrentStep != want {
			t.Errorf("transition %d currentStep = %q, want %q", i, capture.docs[i].CurrentStep, want)
		}
	}

	final := capture.docs[len(capture.docs)-1]
	if final.Status != dto.StatusCompleted {
		t.Errorf("final status = %q, want completed", final.Status)
	}
	if len(final.Steps) != 3 {
		t.Errorf("final embedded step summary = %d entries, want 3", len(final.Steps))
	}
	if final.CompletedAt == nil {
		t.Error("final journey missing completedAt")
	}
}
