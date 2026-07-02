package workflow

import (
	"context"

	"github.com/bureau/onboarding-service/internal/repo"
	"github.com/bureau/onboarding-service/internal/service/dto"
	"github.com/bureau/onboarding-service/internal/service/dto/adapters"
)

// Activities holds the dependencies shared by onboarding Temporal activities.
type Activities struct {
	journeys repo.OnboardingJourneyRepo
}

// NewActivities wires the activities with their dependencies.
func NewActivities(journeys repo.OnboardingJourneyRepo) *Activities {
	return &Activities{journeys: journeys}
}

// PersistJourneyState upserts the denormalised journey read-model in Mongo
// (LLD §5). It is idempotent — Temporal may retry it — because the repo upsert
// is keyed by userId.
func (a *Activities) PersistJourneyState(ctx context.Context, journey dto.OnboardingJourney) error {
	return a.journeys.Upsert(ctx, adapters.ToRepoOnboardingJourney(&journey))
}
