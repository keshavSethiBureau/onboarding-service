package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/bureau/onboarding-service/internal/repo"
	"github.com/bureau/onboarding-service/internal/service/dto"
)

// fakeJourneyRepo captures the doc passed to Upsert so the activity's
// dto->DAO conversion and persistence call can be asserted.
type fakeJourneyRepo struct {
	upserted  *repo.OnboardingJourneyDoc
	upsertErr error
}

func (f *fakeJourneyRepo) Upsert(_ context.Context, doc *repo.OnboardingJourneyDoc) error {
	f.upserted = doc
	return f.upsertErr
}

func (f *fakeJourneyRepo) FindByUserID(context.Context, string) (*repo.OnboardingJourneyDoc, error) {
	return nil, nil
}

func TestPersistJourneyStateActivity(t *testing.T) {
	completed := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		journey    dto.OnboardingJourney
		repoErr    error
		wantErr    bool
		wantSteps  int
		wantStatus string
	}{
		{
			name: "persists in-progress journey",
			journey: dto.OnboardingJourney{
				UserID: "u1", OrgID: "o1", CurrentStep: StepVerticalSelected,
				Status: dto.StatusInProgress, VerticalName: "KYC", StepCatalogVersion: CatalogVersion,
			},
			wantStatus: dto.StatusInProgress,
		},
		{
			name: "persists completed journey with steps",
			journey: dto.OnboardingJourney{
				UserID: "u2", OrgID: "o2", CurrentStep: StepOnboardingCompleted,
				Status: dto.StatusCompleted, VerticalName: "Fraud", StepCatalogVersion: CatalogVersion,
				Steps: []dto.StepSummary{{StepName: StepEmailVerified, Status: dto.StatusCompleted, CompletedAt: &completed}},
			},
			wantStatus: dto.StatusCompleted,
			wantSteps:  1,
		},
		{
			name:    "propagates repo error",
			journey: dto.OnboardingJourney{UserID: "u3", CurrentStep: StepEmailVerified, Status: dto.StatusInProgress},
			repoErr: errors.New("mongo down"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestActivityEnvironment()
			fake := &fakeJourneyRepo{upsertErr: tt.repoErr}
			acts := NewActivities(fake)
			env.RegisterActivity(acts.PersistJourneyState)

			_, err := env.ExecuteActivity(acts.PersistJourneyState, tt.journey)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fake.upserted == nil {
				t.Fatal("Upsert was not called")
			}
			if fake.upserted.UserID != tt.journey.UserID {
				t.Errorf("upserted userId = %q, want %q", fake.upserted.UserID, tt.journey.UserID)
			}
			if fake.upserted.Status != tt.wantStatus {
				t.Errorf("upserted status = %q, want %q", fake.upserted.Status, tt.wantStatus)
			}
			if len(fake.upserted.Steps) != tt.wantSteps {
				t.Errorf("upserted steps = %d, want %d", len(fake.upserted.Steps), tt.wantSteps)
			}
		})
	}
}
