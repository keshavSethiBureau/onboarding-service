package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	mongoconfig "github.com/Bureau-Inc/bureau-commons-go/mongoclient/config"
	enums "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/worker"

	"onboarding-service/internal/repo"
	mongorepo "onboarding-service/internal/repo/mongo"
)

// mongoRepos builds Mongo-backed journey + provisioning repos, skipping when no
// local Mongo is reachable.
func mongoRepos(t *testing.T) (*mongorepo.OnboardingJourneyRepo, *mongorepo.ProvisioningRecordRepo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongoclient.GetOrCreateBureauMongoClient(ctx, &mongoconfig.Config{
		Hosts:          []string{"localhost:27017"},
		Database:       "onboarding_wf_e2e_test",
		DisableMetrics: true,
	}, metricx.NewRegistry())
	if err != nil {
		t.Skipf("no MongoDB reachable (%v); skipping workflow e2e test", err)
	}
	t.Cleanup(func() { _ = client.GetDatabase().Drop(context.Background()) })
	return mongorepo.NewOnboardingJourneyRepo(client), mongorepo.NewProvisioningRecordRepo(client)
}

// e2eActivities wires Activities with real Mongo repos + in-test fakes for the
// external ports (Auth0/provisioners aren't reachable from tests).
func e2eActivities(journeys *mongorepo.OnboardingJourneyRepo, prov *mongorepo.ProvisioningRecordRepo) *Activities {
	return NewActivities(journeys, prov, &countingOrgCreator{}, &countingProvisioner{})
}

// driveAllSignals sends every signal the v1 walk needs (buffered by Temporal
// until each step consumes it), then returns.
func driveAllSignals(t *testing.T, starter *Starter, client interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error
}, userID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := starter.SubmitStep(ctx, userID, "", StepEmailVerified); err != nil {
		t.Fatalf("EMAIL_VERIFIED: %v", err)
	}
	if _, err := starter.RequestOrganisation(ctx, userID, "Acme Inc", "true"); err != nil {
		t.Fatalf("ORGANISATION_CREATED: %v", err)
	}
	_ = client.SignalWorkflow(ctx, userID, "", StepVerticalSelected, SignalPayload{VerticalName: "KYC"})
	_ = client.SignalWorkflow(ctx, userID, "", StepQuestionnaireViewed, SignalPayload{})
	if _, err := starter.RequestComplete(ctx, userID); err != nil {
		t.Fatalf("ONBOARDING_COMPLETED: %v", err)
	}
}

// TestExecutor_FullWalk_EndToEnd drives a v1 journey through every step on a real
// Temporal + Mongo and asserts it ends at RESOURCES_PROVISIONED (completed) with
// all provisioning recorded.
func TestExecutor_FullWalk_EndToEnd(t *testing.T) {
	client := dialTemporal(t)
	defer client.Close()
	journeys, prov := mongoRepos(t)

	w := worker.New(client, TaskQueue, worker.Options{})
	Register(w, e2eActivities(journeys, prov))
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	starter := NewStarter(client, TaskQueue)
	userID := uniqueUserID("walk-e2e")
	orgID := "org_" + userID
	t.Cleanup(func() { _ = client.TerminateWorkflow(context.Background(), userID, "", "test cleanup") })

	driveAllSignals(t, starter, client, userID)

	// Block until the workflow itself completes — the only reliable terminal
	// signal (the read-model is written mid-step, so polling it can observe the
	// terminal step before the final action has run).
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.GetWorkflow(waitCtx, userID, "").Get(waitCtx, nil); err != nil {
		t.Fatalf("workflow did not complete cleanly: %v", err)
	}

	doc, _ := journeys.FindByUserID(context.Background(), userID)
	if doc == nil || doc.CurrentStep != StepResourcesProvisioned || doc.Status != "completed" {
		t.Fatalf("journey did not reach terminal completed state: %+v", doc)
	}

	rec, _ := prov.GetByOrgID(context.Background(), orgID)
	if rec == nil || rec.Status != repo.ProvisioningStatusCompleted {
		t.Fatalf("provisioning record not completed: %+v", rec)
	}
	for _, res := range []string{resourceKong, resourceAWS, resourceSvix, resourceLago} {
		if _, ok := rec.Resources[res]; !ok {
			t.Errorf("missing provisioned resource %q in %+v", res, rec.Resources)
		}
	}
}

// TestExecutor_ReplayIsDeterministic runs a full journey, fetches its history,
// and replays it with WorkflowReplayer. A clean replay proves the workflow is
// deterministic and that a crash-and-replay resumes from history WITHOUT
// re-running completed steps (Temporal feeds their recorded results).
func TestExecutor_ReplayIsDeterministic(t *testing.T) {
	client := dialTemporal(t)
	defer client.Close()
	journeys, prov := mongoRepos(t)

	w := worker.New(client, TaskQueue, worker.Options{})
	Register(w, e2eActivities(journeys, prov))
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	starter := NewStarter(client, TaskQueue)
	userID := uniqueUserID("replay-e2e")
	t.Cleanup(func() { _ = client.TerminateWorkflow(context.Background(), userID, "", "test cleanup") })

	driveAllSignals(t, starter, client, userID)

	// Wait for completion so the history is a full, terminal run.
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.GetWorkflow(waitCtx, userID, "").Get(waitCtx, nil); err != nil {
		t.Fatalf("workflow did not complete cleanly: %v", err)
	}

	// Collect the full history.
	ctx := context.Background()
	iter := client.GetWorkflowHistory(ctx, userID, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	hist := &historypb.History{}
	for iter.HasNext() {
		e, err := iter.Next()
		if err != nil {
			t.Fatalf("history next: %v", err)
		}
		hist.Events = append(hist.Events, e)
	}
	if len(hist.Events) == 0 {
		t.Fatal("empty history")
	}

	// Replay against the current workflow code — errors on any non-determinism.
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(OnboardingWorkflow)
	if err := replayer.ReplayWorkflowHistory(nil, hist); err != nil {
		t.Fatalf("replay was non-deterministic / failed to resume: %v", err)
	}
}
