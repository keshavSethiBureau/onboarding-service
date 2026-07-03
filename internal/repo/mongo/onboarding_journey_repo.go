// Package mongo holds the Mongo-backed implementations of the repo ports,
// built on the commons mongoclient.
package mongo

import (
	"context"
	"errors"
	"time"

	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"onboarding-service/internal/repo"
)

// OnboardingJourneyRepo is the Mongo implementation of repo.OnboardingJourneyRepo.
type OnboardingJourneyRepo struct {
	client *mongoclient.BureauMongoClient
}

// NewOnboardingJourneyRepo returns a Mongo-backed journey repository.
func NewOnboardingJourneyRepo(client *mongoclient.BureauMongoClient) *OnboardingJourneyRepo {
	return &OnboardingJourneyRepo{client: client}
}

// Upsert idempotently writes the journey read-model, keyed by userId. It uses
// userId as the document _id (a stable string key) and replaces the whole
// document, so re-running never creates a duplicate (the unique userId index is
// belt-and-suspenders).
func (r *OnboardingJourneyRepo) Upsert(ctx context.Context, doc *repo.OnboardingJourneyDoc) error {
	if doc.ID == "" {
		doc.ID = doc.UserID
	}
	doc.UpdatedAt = time.Now().UTC()

	result := r.client.ReplaceOne(
		ctx,
		repo.CollOnboardingJourneys,
		bson.M{"userId": doc.UserID},
		doc,
		options.Replace().SetUpsert(true),
	)
	if !result.Success {
		return result.Error
	}
	return nil
}

// SetOrgID sets only the orgId (upsert keyed by userId), leaving the rest of the
// journey untouched. $setOnInsert pins _id=userId so a fresh doc gets a stable
// string id (no ObjectID) if org creation precedes any step.
func (r *OnboardingJourneyRepo) SetOrgID(ctx context.Context, userID, orgID string) error {
	now := time.Now().UTC()
	result := r.client.UpdateOne(
		ctx,
		repo.CollOnboardingJourneys,
		bson.M{"userId": userID},
		bson.M{
			"$set": bson.M{"orgId": orgID, "updatedAt": now},
			"$setOnInsert": bson.M{
				"_id": userID, "status": "in_progress", "startedAt": now,
			},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if !result.Success {
		return result.Error
	}
	return nil
}

// FindByUserID returns the journey for a user, or (nil, nil) if none exists yet.
func (r *OnboardingJourneyRepo) FindByUserID(ctx context.Context, userID string) (*repo.OnboardingJourneyDoc, error) {
	doc, err := mongoclient.FindOneGeneric[repo.OnboardingJourneyDoc](
		r.client, ctx, repo.CollOnboardingJourneys, bson.M{"userId": userID},
	)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}
