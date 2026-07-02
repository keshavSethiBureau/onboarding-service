// Package adapters converts between repo DAO structs and service DTOs, keeping
// the layers decoupled (LLD §12). write: dto -> ToRepoX -> repo.XDoc;
// read: repo.XDoc -> FromRepoX -> dto.
package adapters

import (
	"github.com/bureau/onboarding-service/internal/repo"
	"github.com/bureau/onboarding-service/internal/service/dto"
)

// ToRepoOnboardingJourney converts a service DTO to the Mongo DAO, including the
// embedded step summaries.
func ToRepoOnboardingJourney(j *dto.OnboardingJourney) *repo.OnboardingJourneyDoc {
	if j == nil {
		return nil
	}
	var steps []repo.StepSummaryDoc
	if j.Steps != nil {
		steps = make([]repo.StepSummaryDoc, len(j.Steps))
		for i, s := range j.Steps {
			steps[i] = repo.StepSummaryDoc{
				StepName:    s.StepName,
				Status:      s.Status,
				CompletedAt: s.CompletedAt,
			}
		}
	}
	return &repo.OnboardingJourneyDoc{
		ID:                 j.ID,
		UserID:             j.UserID,
		OrgID:              j.OrgID,
		CurrentStep:        j.CurrentStep,
		Status:             j.Status,
		VerticalName:       j.VerticalName,
		StepCatalogVersion: j.StepCatalogVersion,
		Steps:              steps,
		StartedAt:          j.StartedAt,
		CompletedAt:        j.CompletedAt,
		UpdatedAt:          j.UpdatedAt,
	}
}

// FromRepoOnboardingJourney converts the Mongo DAO back to a service DTO.
func FromRepoOnboardingJourney(d *repo.OnboardingJourneyDoc) *dto.OnboardingJourney {
	if d == nil {
		return nil
	}
	var steps []dto.StepSummary
	if d.Steps != nil {
		steps = make([]dto.StepSummary, len(d.Steps))
		for i, s := range d.Steps {
			steps[i] = dto.StepSummary{
				StepName:    s.StepName,
				Status:      s.Status,
				CompletedAt: s.CompletedAt,
			}
		}
	}
	return &dto.OnboardingJourney{
		ID:                 d.ID,
		UserID:             d.UserID,
		OrgID:              d.OrgID,
		CurrentStep:        d.CurrentStep,
		Status:             d.Status,
		VerticalName:       d.VerticalName,
		StepCatalogVersion: d.StepCatalogVersion,
		Steps:              steps,
		StartedAt:          d.StartedAt,
		CompletedAt:        d.CompletedAt,
		UpdatedAt:          d.UpdatedAt,
	}
}
