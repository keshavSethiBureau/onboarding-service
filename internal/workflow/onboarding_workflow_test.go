package workflow

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"onboarding-service/internal/repo"
)

// captureRepo records every persisted doc so the test can inspect the sequence
// of currentStep values the executor walked.
type captureRepo struct {
	docs []*repo.OnboardingJourneyDoc
}

func (c *captureRepo) Upsert(_ context.Context, doc *repo.OnboardingJourneyDoc) error {
	cp := *doc
	c.docs = append(c.docs, &cp)
	return nil
}
func (c *captureRepo) FindByUserID(context.Context, string) (*repo.OnboardingJourneyDoc, error) {
	return nil, nil
}
func (c *captureRepo) SetOrgID(context.Context, string, string) error { return nil }

// TestExecutor_WalksV1InOrder proves the generic executor walks the v1 catalog in
// order: currentStep is persisted for each step in catalog sequence, and the
// terminal step marks the journey completed.
func TestExecutor_WalksV1InOrder(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	capture := &captureRepo{}
	acts := NewActivities(capture, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{})
	Register(env, acts)

	// Drive every user-driven step's signal so the walk runs to completion.
	for i, step := range CatalogSteps(CatalogVersion) {
		if step.Signal == "" {
			continue
		}
		s := step.Signal
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(s, SignalPayload{DisplayName: "Acme"})
		}, time.Duration(i+1)*time.Millisecond)
	}

	env.ExecuteWorkflow(OnboardingWorkflow, WorkflowInput{UserID: "u1", StepCatalogVersion: CatalogVersion})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	// The distinct currentStep values persisted, in first-seen order, must equal
	// the catalog's step names in order.
	var gotOrder []string
	seen := map[string]bool{}
	for _, d := range capture.docs {
		if !seen[d.CurrentStep] {
			seen[d.CurrentStep] = true
			gotOrder = append(gotOrder, d.CurrentStep)
		}
	}
	var want []string
	for _, step := range CatalogSteps(CatalogVersion) {
		want = append(want, step.Name)
	}
	if !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("walk order = %v, want %v", gotOrder, want)
	}

	// Final persisted state: completed, terminal step, full step summary.
	final := capture.docs[len(capture.docs)-1]
	if final.Status != "completed" {
		t.Errorf("final status = %q, want completed", final.Status)
	}
	if final.CurrentStep != StepResourcesProvisioned {
		t.Errorf("final currentStep = %q, want RESOURCES_PROVISIONED", final.CurrentStep)
	}
	if len(final.Steps) != len(want) {
		t.Errorf("final step summary = %d entries, want %d", len(final.Steps), len(want))
	}
}

// TestCatalogV1_Immutable is a golden test: it locks the exact contents of
// catalog v1 so an accidental edit to a shipped version fails CI (the version
// must be immutable — a change means a NEW version).
func TestCatalogV1_Immutable(t *testing.T) {
	want := []StepDef{
		{Name: "EMAIL_VERIFIED", Signal: "EMAIL_VERIFIED"},
		{Name: "ORGANISATION_CREATED", Signal: "ORGANISATION_CREATED", Action: "CreateOrganisation"},
		{Name: "PROVISION_KONG", Action: "ProvisionKong"},
		{Name: "PROVISION_AWS", Action: "ProvisionAWS"},
		{Name: "VERTICAL_SELECTED", Signal: "VERTICAL_SELECTED"},
		{Name: "QUESTIONNAIRE_VIEWED", Signal: "QUESTIONNAIRE_VIEWED"},
		{Name: "ONBOARDING_COMPLETED", Signal: "ONBOARDING_COMPLETED", MarksComplete: true},
		{Name: "PROVISION_SVIX", Action: "ProvisionSvix"},
		{Name: "PROVISION_LAGO", Action: "ProvisionLago"},
		{Name: "RESOURCES_PROVISIONED", Action: "CompleteProvisioning"},
	}
	if !reflect.DeepEqual(CatalogSteps(1), want) {
		t.Fatalf("catalog v1 changed — versions are immutable; add a NEW version instead.\n got: %+v\nwant: %+v", CatalogSteps(1), want)
	}
}
