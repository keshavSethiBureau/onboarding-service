package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
)

// captureSink records every emitted event and can fail on demand.
type captureSink struct {
	events []StepEvent
	err    error
}

func (c *captureSink) Emit(_ context.Context, evt StepEvent) error {
	if c.err != nil {
		return c.err
	}
	c.events = append(c.events, evt)
	return nil
}

func TestEmitStepEvent_ForwardsToSink(t *testing.T) {
	sink := &captureSink{}
	acts := NewActivities(&stubJourneyRepo{}, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{}).
		WithStepEventSink(sink)

	at := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	evt := StepEvent{UserID: "user1", OrgID: "org1", Step: StepProvisionKong, Timestamp: at}
	if err := acts.EmitStepEvent(context.Background(), evt); err != nil {
		t.Fatalf("EmitStepEvent: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("sink received %d events, want 1", len(sink.events))
	}
	if got := sink.events[0]; got != evt {
		t.Errorf("sink event = %+v, want %+v", got, evt)
	}
}

func TestEmitStepEvent_SinkErrorSurfaces(t *testing.T) {
	sink := &captureSink{err: errors.New("sink down")}
	acts := NewActivities(&stubJourneyRepo{}, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{}).
		WithStepEventSink(sink)

	if err := acts.EmitStepEvent(context.Background(), StepEvent{UserID: "user1"}); err == nil {
		t.Fatal("expected sink error to surface (so Temporal retries the emit)")
	}
}

// TestWorkflow_EmitsOneEventPerStep runs the full v1 walk against the stub sink
// and proves every step transition produces exactly one step-event, in catalog
// order, with orgId populated once the org exists.
func TestWorkflow_EmitsOneEventPerStep(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	sink := &captureSink{}
	acts := NewActivities(&captureRepo{}, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{}).
		WithStepEventSink(sink)
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
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	steps := CatalogSteps(CatalogVersion)
	if len(sink.events) != len(steps) {
		t.Fatalf("sink received %d events, want exactly %d (one per step)", len(sink.events), len(steps))
	}
	var gotSteps, wantSteps []string
	for i, evt := range sink.events {
		gotSteps = append(gotSteps, evt.Step)
		wantSteps = append(wantSteps, steps[i].Name)
		if evt.UserID != "u1" {
			t.Errorf("event %d userId = %q, want u1", i, evt.UserID)
		}
		if evt.Timestamp.IsZero() {
			t.Errorf("event %d (%s) has zero timestamp", i, evt.Step)
		}
		// countingOrgCreator returns "org_u1" at ORGANISATION_CREATED (index 1);
		// every event from then on must carry it, the ones before must not.
		if wantOrg := i >= 1; (evt.OrgID == "org_u1") != wantOrg {
			t.Errorf("event %d (%s) orgId = %q, want populated=%v", i, evt.Step, evt.OrgID, wantOrg)
		}
	}
	if !reflect.DeepEqual(gotSteps, wantSteps) {
		t.Fatalf("event step order = %v, want %v", gotSteps, wantSteps)
	}
}

func TestEmitStepEvent_DefaultsToLogSink(t *testing.T) {
	// NewActivities without an explicit sink must not panic — the log-only
	// placeholder is wired until a real analytics transport is chosen.
	acts := NewActivities(&stubJourneyRepo{}, newFakeProvisioningRepo(), &countingOrgCreator{}, &countingProvisioner{})
	if err := acts.EmitStepEvent(context.Background(), StepEvent{
		UserID: "user1", OrgID: "org1", Step: StepEmailVerified, Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("log sink should always succeed, got %v", err)
	}
}
