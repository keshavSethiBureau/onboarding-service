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

	// Deliver steps, including a DUPLICATE EMAIL_VERIFIED (must be a no-op) and
	// running through the terminal step RESOURCES_PROVISIONED.
	signals := []struct {
		at   time.Duration
		step string
	}{
		{1 * time.Millisecond, StepEmailVerified},
		{2 * time.Millisecond, StepVerticalSelected},
		{3 * time.Millisecond, StepEmailVerified}, // duplicate -> ignored
		{4 * time.Millisecond, StepOnboardingCompleted},
		{5 * time.Millisecond, StepResourcesProvisioned},
	}
	for _, s := range signals {
		s := s
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(StepSignalName, StepSignal{StepName: s.step, OrgID: "org-1"})
		}, s.at)
	}

	env.ExecuteWorkflow(OnboardingWorkflow, WorkflowInput{UserID: "user-1", OrgID: "org-1"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	// 5 signals but only 4 real transitions -> the duplicate EMAIL_VERIFIED
	// persisted nothing (effect-idempotent).
	if len(capture.docs) != 4 {
		t.Fatalf("expected 4 persisted transitions (duplicate ignored), got %d", len(capture.docs))
	}

	for i, d := range capture.docs {
		if d.StepCatalogVersion != CatalogVersion {
			t.Errorf("doc[%d] catalog version = %d, want %d (pinned)", i, d.StepCatalogVersion, CatalogVersion)
		}
		if d.UserID != "user-1" || d.OrgID != "org-1" {
			t.Errorf("doc[%d] identity = %s/%s", i, d.UserID, d.OrgID)
		}
	}

	// currentStep tracks furthest progress and never regresses on the duplicate.
	wantProgress := []string{StepEmailVerified, StepVerticalSelected, StepOnboardingCompleted, StepResourcesProvisioned}
	for i, want := range wantProgress {
		if capture.docs[i].CurrentStep != want {
			t.Errorf("transition %d currentStep = %q, want %q", i, capture.docs[i].CurrentStep, want)
		}
	}

	// Journey is marked completed at ONBOARDING_COMPLETED (3rd transition)...
	if capture.docs[2].Status != dto.StatusCompleted || capture.docs[2].CompletedAt == nil {
		t.Errorf("expected completed status at ONBOARDING_COMPLETED, got %+v", capture.docs[2])
	}

	// ...and the final state has all 4 distinct steps in the embedded summary.
	final := capture.docs[3]
	if len(final.Steps) != 4 {
		t.Errorf("final embedded step summary = %d entries, want 4 (deduped)", len(final.Steps))
	}
}
