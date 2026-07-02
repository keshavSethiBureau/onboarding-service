package adapters

import (
	"reflect"
	"testing"
	"time"

	"github.com/bureau/onboarding-service/internal/service/dto"
)

func TestOnboardingJourneyRoundTrip(t *testing.T) {
	started := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 7, 2, 11, 5, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   *dto.OnboardingJourney
	}{
		{name: "nil", in: nil},
		{
			name: "no steps",
			in: &dto.OnboardingJourney{
				ID: "u1", UserID: "u1", OrgID: "o1",
				CurrentStep: "VERTICAL_SELECTED", Status: dto.StatusInProgress,
				VerticalName: "KYC", StepCatalogVersion: 1,
				StartedAt: started, UpdatedAt: updated,
			},
		},
		{
			name: "with embedded steps",
			in: &dto.OnboardingJourney{
				ID: "u2", UserID: "u2", OrgID: "o2",
				CurrentStep: "ONBOARDING_COMPLETED", Status: dto.StatusCompleted,
				VerticalName: "Fraud", StepCatalogVersion: 1,
				Steps: []dto.StepSummary{
					{StepName: "EMAIL_VERIFIED", Status: dto.StatusCompleted, CompletedAt: &completed},
					{StepName: "VERTICAL_SELECTED", Status: dto.StatusCompleted, CompletedAt: &completed},
				},
				StartedAt: started, CompletedAt: &completed, UpdatedAt: updated,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := ToRepoOnboardingJourney(tt.in)
			out := FromRepoOnboardingJourney(doc)

			if tt.in == nil {
				if doc != nil || out != nil {
					t.Fatalf("nil input should map to nil, got doc=%v out=%v", doc, out)
				}
				return
			}
			// DAO must mirror the DTO field-for-field...
			if doc.UserID != tt.in.UserID || doc.CurrentStep != tt.in.CurrentStep ||
				doc.Status != tt.in.Status || doc.VerticalName != tt.in.VerticalName ||
				len(doc.Steps) != len(tt.in.Steps) {
				t.Errorf("ToRepo mismatch: %+v", doc)
			}
			// ...and the round-trip must reproduce the original DTO exactly.
			if !reflect.DeepEqual(tt.in, out) {
				t.Errorf("round-trip mismatch:\n in=%+v\nout=%+v", tt.in, out)
			}
		})
	}
}
