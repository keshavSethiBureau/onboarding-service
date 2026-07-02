package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	mongoconfig "github.com/Bureau-Inc/bureau-commons-go/mongoclient/config"
	"go.temporal.io/sdk/worker"

	"github.com/bureau/onboarding-service/internal/repo"
	mongorepo "github.com/bureau/onboarding-service/internal/repo/mongo"
)

// mongoJourneyRepo builds a Mongo-backed journey repo against a local Mongo,
// skipping when none is reachable. Used to prove the end-to-end read-model write.
func mongoJourneyRepo(t *testing.T) *mongorepo.OnboardingJourneyRepo {
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
	return mongorepo.NewOnboardingJourneyRepo(client)
}

// TestEmailVerified_StartsWorkflowAndWritesReadModel drives the real path:
// a worker runs the workflow + PersistJourneyState against a real Temporal and
// Mongo. A first EMAIL_VERIFIED must start the workflow and write the journey
// read-model with currentStep = EMAIL_VERIFIED; a second EMAIL_VERIFIED for the
// same user must NOT create a second workflow or journey (idempotent start).
func TestEmailVerified_StartsWorkflowAndWritesReadModel(t *testing.T) {
	client := dialTemporal(t)
	defer client.Close()
	journeys := mongoJourneyRepo(t)

	w := worker.New(client, TaskQueue, worker.Options{})
	Register(w, NewActivities(journeys))
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	starter := NewStarter(client, TaskQueue)
	ctx := context.Background()
	userID := uniqueUserID("wf-e2e")
	t.Cleanup(func() { _ = client.TerminateWorkflow(context.Background(), userID, "", "test cleanup") })

	// First EMAIL_VERIFIED -> starts the workflow.
	run1, err := starter.SubmitStep(ctx, userID, "org-1", StepEmailVerified)
	if err != nil {
		t.Fatalf("first SubmitStep: %v", err)
	}

	// The worker processes the signal and PersistJourneyState writes the read-model.
	doc := waitForJourney(t, journeys, userID)
	if doc.CurrentStep != StepEmailVerified {
		t.Errorf("currentStep = %q, want EMAIL_VERIFIED", doc.CurrentStep)
	}
	if doc.Status != "in_progress" {
		t.Errorf("status = %q, want in_progress", doc.Status)
	}
	if doc.StepCatalogVersion != CatalogVersion {
		t.Errorf("stepCatalogVersion = %d, want %d (pinned)", doc.StepCatalogVersion, CatalogVersion)
	}
	if len(doc.Steps) != 1 {
		t.Errorf("step summary = %d entries, want 1", len(doc.Steps))
	}

	// Second EMAIL_VERIFIED for the same user -> same workflow (no duplicate).
	run2, err := starter.SubmitStep(ctx, userID, "org-1", StepEmailVerified)
	if err != nil {
		t.Fatalf("second SubmitStep: %v", err)
	}
	if run2 != run1 {
		t.Fatalf("second EMAIL_VERIFIED started a new run (%q != %q); idempotent start violated", run2, run1)
	}
	// Give the re-delivered signal time to process, then assert still ONE journey,
	// still a single de-duped step entry.
	time.Sleep(500 * time.Millisecond)
	got, err := journeys.FindByUserID(ctx, userID)
	if err != nil || got == nil {
		t.Fatalf("FindByUserID after second call: got=%v err=%v", got, err)
	}
	if len(got.Steps) != 1 {
		t.Errorf("after duplicate EMAIL_VERIFIED, step summary = %d entries, want 1 (idempotent)", len(got.Steps))
	}
	if got.CurrentStep != StepEmailVerified {
		t.Errorf("currentStep after duplicate = %q, want EMAIL_VERIFIED", got.CurrentStep)
	}
}

func waitForJourney(t *testing.T, r *mongorepo.OnboardingJourneyRepo, userID string) *repo.OnboardingJourneyDoc {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		doc, err := r.FindByUserID(ctx, userID)
		if err != nil {
			t.Fatalf("FindByUserID: %v", err)
		}
		if doc != nil {
			return doc
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("journey read-model for %s was not written in time", userID)
	return nil
}
