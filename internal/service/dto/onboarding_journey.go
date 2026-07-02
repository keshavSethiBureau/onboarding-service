package dto

import "time"

// Journey/step status values (LLD §3, closed sets).
const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

// OnboardingJourney is the service-layer view of the journey read-model. It is
// converted to/from repo.OnboardingJourneyDoc by the adapters and never shares
// a struct with the repo or view layers (LLD §12).
type OnboardingJourney struct {
	ID                 string
	UserID             string
	OrgID              string
	CurrentStep        string
	Status             string
	VerticalName       string
	StepCatalogVersion int
	Steps              []StepSummary
	StartedAt          time.Time
	CompletedAt        *time.Time
	UpdatedAt          time.Time
}

// StepSummary is the service-layer per-step summary.
type StepSummary struct {
	StepName    string
	Status      string
	CompletedAt *time.Time
}
