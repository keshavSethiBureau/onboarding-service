package workflow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"go.temporal.io/sdk/testsuite"

	"onboarding-service/internal/observability"
)

// TestObservability_MetricsAfterOneJourney drives a full v1 journey through the
// Temporal test env with metrics wired, then renders /metrics and asserts the
// onboarding_ instruments are present and populated. The emitted onboarding_
// lines are logged as the proof artifact.
func TestObservability_MetricsAfterOneJourney(t *testing.T) {
	reg := metricx.NewRegistry()
	m := observability.NewMetrics(reg)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	acts := NewActivities(&captureRepo{}, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{}).
		WithStepEventSink(&captureSink{}).
		WithMetrics(m)
	Register(env, acts)

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
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow did not complete cleanly: %v", env.GetWorkflowError())
	}

	// Render /metrics exactly as the scrape endpoint would.
	rec := httptest.NewRecorder()
	metricx.NewHandler(reg, &metricx.Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	// Every new instrument must be present after one journey.
	want := []string{
		"onboarding_step_transitions_total",
		"onboarding_workflow_started_total",
		"onboarding_workflow_completed_total",
		"onboarding_activity_executions_total",
		"onboarding_activity_duration_seconds",
		"onboarding_provisioning_total",
		"onboarding_provisioning_duration_seconds",
	}
	for _, name := range want {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics missing %s", name)
		}
	}
	// Funnel + lifecycle values.
	if !strings.Contains(body, `onboarding_workflow_started_total 1`) {
		t.Error("workflow_started_total != 1")
	}
	if !strings.Contains(body, `onboarding_workflow_completed_total 1`) {
		t.Error("workflow_completed_total != 1")
	}
	if !strings.Contains(body, `onboarding_step_transitions_total{status="completed",step="EMAIL_VERIFIED"} 1`) {
		t.Error("expected one completed EMAIL_VERIFIED step transition")
	}
	if !strings.Contains(body, `onboarding_provisioning_total{resource="lago",status="success"} 1`) {
		t.Error("expected one successful lago provisioning")
	}

	// Proof artifact: dump the onboarding_ sample lines.
	t.Log("---- /metrics (onboarding_ samples) ----")
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "onboarding_") {
			t.Log(line)
		}
	}
}
