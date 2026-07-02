package impl

import (
	"context"

	"github.com/bureau/onboarding-service/internal/repo"
	"github.com/bureau/onboarding-service/internal/service/dto"
	"github.com/bureau/onboarding-service/internal/service/dto/adapters"
	"github.com/bureau/onboarding-service/internal/workflow"
)

// OnboardingService is the business-logic layer for onboarding reads.
type OnboardingService struct {
	journeys repo.OnboardingJourneyRepo
}

// NewOnboardingService returns a service backed by the journey repository.
func NewOnboardingService(journeys repo.OnboardingJourneyRepo) *OnboardingService {
	return &OnboardingService{journeys: journeys}
}

// GetState returns the user's journey read-model. If no journey exists yet, it
// returns a synthetic first-step state (LLD §8: "if missing, return the first
// step") so the frontend can route a fresh user.
func (s *OnboardingService) GetState(ctx context.Context, userID string) (*dto.OnboardingJourney, error) {
	doc, err := s.journeys.FindByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return &dto.OnboardingJourney{
			UserID:      userID,
			CurrentStep: workflow.FirstStep(),
			Status:      dto.StatusInProgress,
		}, nil
	}
	return adapters.FromRepoOnboardingJourney(doc), nil
}
