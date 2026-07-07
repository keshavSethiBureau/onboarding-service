package repo

import (
	"context"
	"fmt"

	"github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection names owned by this service (its own Mongo DB).
const (
	CollOnboardingJourneys  = "onboarding_journeys"
	CollOnboardingSteps     = "onboarding_steps"
	CollProvisioningRecords = "provisioning_records"
	CollStepCatalogs        = "step_catalogs"
)

// EnsureIndexes creates the indexes described in the LLD on startup:
//   - onboarding_journeys:  unique index on userId
//   - onboarding_steps:     index on journeyId
//   - provisioning_records: unique index on orgId
//   - step_catalogs:        unique index on version (insert-only collection)
//
// CreateOne is idempotent for an identical index spec, so this is safe to run
// on every boot.
func EnsureIndexes(ctx context.Context, m *mongoclient.BureauMongoClient) error {
	indexes := []struct {
		collection string
		model      mongo.IndexModel
	}{
		{
			collection: CollOnboardingJourneys,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "userId", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_userId"),
			},
		},
		{
			collection: CollOnboardingSteps,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "journeyId", Value: 1}},
				Options: options.Index().SetName("idx_journeyId"),
			},
		},
		{
			collection: CollProvisioningRecords,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "orgId", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_orgId"),
			},
		},
		{
			// Insert-only catalog versions: the unique index is what makes the
			// max+1 allocation race-safe (the loser's insert fails and retries).
			collection: CollStepCatalogs,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "version", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("uniq_version"),
			},
		},
	}

	for _, idx := range indexes {
		if _, err := m.GetCollection(idx.collection).Indexes().CreateOne(ctx, idx.model); err != nil {
			return fmt.Errorf("create index on %s: %w", idx.collection, err)
		}
	}
	return nil
}
