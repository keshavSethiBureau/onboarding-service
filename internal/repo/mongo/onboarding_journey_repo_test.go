package mongo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	mongoconfig "github.com/Bureau-Inc/bureau-commons-go/mongoclient/config"

	"onboarding-service/internal/repo"
)

// testRepo connects to a local Mongo (MONGO_TEST_URI host or localhost:27017) in
// an isolated database, skipping the test when no Mongo is reachable.
func testRepo(t *testing.T) (*OnboardingJourneyRepo, *mongoclient.BureauMongoClient) {
	t.Helper()
	host := os.Getenv("MONGO_TEST_HOST")
	if host == "" {
		host = "localhost:27017"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := mongoclient.GetOrCreateBureauMongoClient(ctx, &mongoconfig.Config{
		Hosts:          []string{host},
		Database:       "onboarding_repo_test",
		DisableMetrics: true,
	}, metricx.NewRegistry())
	if err != nil {
		t.Skipf("no MongoDB reachable at %s (%v); skipping repo integration test", host, err)
	}
	t.Cleanup(func() {
		_ = client.GetDatabase().Drop(context.Background())
	})
	return NewOnboardingJourneyRepo(client), client
}

func TestOnboardingJourneyRepo(t *testing.T) {
	r, client := testRepo(t)
	ctx := context.Background()
	completed := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)

	t.Run("upsert then find", func(t *testing.T) {
		doc := &repo.OnboardingJourneyDoc{
			UserID: "user-1", OrgID: "org-1", CurrentStep: "VERTICAL_SELECTED",
			Status: "in_progress", VerticalName: "KYC", StepCatalogVersion: 1,
			Steps:     []repo.StepSummaryDoc{{StepName: "EMAIL_VERIFIED", Status: "completed", CompletedAt: &completed}},
			StartedAt: completed,
		}
		if err := r.Upsert(ctx, doc); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := r.FindByUserID(ctx, "user-1")
		if err != nil || got == nil {
			t.Fatalf("FindByUserID: got=%v err=%v", got, err)
		}
		if got.CurrentStep != "VERTICAL_SELECTED" || got.VerticalName != "KYC" || len(got.Steps) != 1 {
			t.Errorf("unexpected doc: %+v", got)
		}
		if got.ID != "user-1" {
			t.Errorf("expected _id = userId (user-1), got %q", got.ID)
		}
	})

	t.Run("upsert is idempotent (updates, no duplicate)", func(t *testing.T) {
		doc := &repo.OnboardingJourneyDoc{UserID: "user-2", CurrentStep: "EMAIL_VERIFIED", Status: "in_progress"}
		if err := r.Upsert(ctx, doc); err != nil {
			t.Fatalf("first upsert: %v", err)
		}
		doc.CurrentStep = "ONBOARDING_COMPLETED"
		doc.Status = "completed"
		if err := r.Upsert(ctx, doc); err != nil {
			t.Fatalf("second upsert: %v", err)
		}
		count, err := client.CountDocuments(ctx, repo.CollOnboardingJourneys, map[string]any{"userId": "user-2"})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected exactly 1 doc for user-2, got %d", count)
		}
		got, _ := r.FindByUserID(ctx, "user-2")
		if got == nil || got.CurrentStep != "ONBOARDING_COMPLETED" || got.Status != "completed" {
			t.Errorf("update not reflected: %+v", got)
		}
	})

	t.Run("find missing returns nil", func(t *testing.T) {
		got, err := r.FindByUserID(ctx, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil for missing user, got %+v", got)
		}
	})
}
