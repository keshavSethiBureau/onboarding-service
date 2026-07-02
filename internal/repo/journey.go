package repo

import (
	"context"
	"time"
)

// OnboardingJourneyDoc is the DAO (Mongo storage shape) for the denormalised
// onboarding journey read-model (LLD §6.1). One document per user, keyed by
// userId; the completed-step summary is embedded.
type OnboardingJourneyDoc struct {
	ID                 string           `json:"id" bson:"_id,omitempty"`
	UserID             string           `json:"userId" bson:"userId"`
	OrgID              string           `json:"orgId" bson:"orgId"`
	CurrentStep        string           `json:"currentStep" bson:"currentStep"`
	Status             string           `json:"status" bson:"status"`
	VerticalName       string           `json:"verticalName" bson:"verticalName"`
	StepCatalogVersion int              `json:"stepCatalogVersion" bson:"stepCatalogVersion"`
	Steps              []StepSummaryDoc `json:"steps" bson:"steps"`
	StartedAt          time.Time        `json:"startedAt" bson:"startedAt"`
	CompletedAt        *time.Time       `json:"completedAt" bson:"completedAt"`
	UpdatedAt          time.Time        `json:"updatedAt" bson:"updatedAt"`
}

// StepSummaryDoc is the embedded per-step summary within a journey document.
type StepSummaryDoc struct {
	StepName    string     `json:"stepName" bson:"stepName"`
	Status      string     `json:"status" bson:"status"`
	CompletedAt *time.Time `json:"completedAt" bson:"completedAt"`
}

// OnboardingJourneyRepo is the persistence port for the journey read-model.
// FindByUserID returns (nil, nil) when no journey exists yet.
type OnboardingJourneyRepo interface {
	Upsert(ctx context.Context, doc *OnboardingJourneyDoc) error
	FindByUserID(ctx context.Context, userID string) (*OnboardingJourneyDoc, error)
}
