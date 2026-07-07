package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"onboarding-service/internal/repo"
)

// createVersionAttempts bounds the insert-retry loop: each retry means a
// concurrent creation won the unique-version race, which is rare — beyond this
// the caller gets ErrCatalogVersionRace.
const createVersionAttempts = 3

// StepCatalogRepo is the Mongo implementation of repo.StepCatalogRepo. The
// step_catalogs collection is INSERT-ONLY (unique index on version); update and
// delete are rejected unconditionally.
type StepCatalogRepo struct {
	client *mongoclient.BureauMongoClient
}

// NewStepCatalogRepo returns a Mongo-backed step catalog repository.
func NewStepCatalogRepo(client *mongoclient.BureauMongoClient) *StepCatalogRepo {
	return &StepCatalogRepo{client: client}
}

// maxVersion returns the highest version in the collection (0 when empty).
// It is max(version) — deliberately NOT a document count: count can diverge
// from max under races or anomalies and must never derive a version number.
func (r *StepCatalogRepo) maxVersion(ctx context.Context) (int, error) {
	doc, err := mongoclient.FindOneGeneric[repo.StepCatalogDoc](
		r.client, ctx, repo.CollStepCatalogs, bson.M{},
		options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}}),
	)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, nil
		}
		return 0, err
	}
	return doc.Version, nil
}

// CreateVersion allocates the next catalog version with the insert-retry
// algorithm:
//
//  1. read currentMax = max(version)
//  2. INSERT {version: currentMax+1, steps, createdAt}
//  3. if the insert loses the unique-index race to a concurrent creation,
//     re-read currentMax and retry from 2 (bounded)
//
// Existing versions are never touched.
func (r *StepCatalogRepo) CreateVersion(ctx context.Context, steps []repo.StepDefDoc) (int, error) {
	for attempt := 0; attempt < createVersionAttempts; attempt++ {
		currentMax, err := r.maxVersion(ctx)
		if err != nil {
			return 0, fmt.Errorf("read max catalog version: %w", err)
		}
		next := currentMax + 1

		res := r.client.InsertOne(ctx, repo.CollStepCatalogs, repo.StepCatalogDoc{
			ID:        fmt.Sprintf("v%d", next),
			Version:   next,
			Steps:     steps,
			CreatedAt: time.Now().UTC(),
		})
		if res.Success {
			return next, nil
		}
		if mongo.IsDuplicateKeyError(res.Error) {
			continue // a concurrent creation won this version — re-read max and retry
		}
		return 0, fmt.Errorf("insert catalog version %d: %w", next, res.Error)
	}
	return 0, repo.ErrCatalogVersionRace
}

// EnsureVersion idempotently seeds a specific known version. Unlike
// CreateVersion (which allocates max+1), this inserts the exact version given
// and treats a duplicate-version insert as success — so concurrent instances
// seeding the same deployed baseline never diverge into extra versions. It
// never updates the existing document (insert-only immutability).
func (r *StepCatalogRepo) EnsureVersion(ctx context.Context, version int, steps []repo.StepDefDoc) error {
	res := r.client.InsertOne(ctx, repo.CollStepCatalogs, repo.StepCatalogDoc{
		ID:        fmt.Sprintf("v%d", version),
		Version:   version,
		Steps:     steps,
		CreatedAt: time.Now().UTC(),
	})
	if res.Success || mongo.IsDuplicateKeyError(res.Error) {
		return nil // already seeded — leave the existing version untouched
	}
	return fmt.Errorf("seed catalog version %d: %w", version, res.Error)
}

// LoadAll returns every catalog version, ordered by version ascending.
func (r *StepCatalogRepo) LoadAll(ctx context.Context) ([]repo.StepCatalogDoc, error) {
	return mongoclient.FindGeneric[repo.StepCatalogDoc](
		r.client, ctx, repo.CollStepCatalogs, bson.M{},
		options.Find().SetSort(bson.D{{Key: "version", Value: 1}}),
	)
}

// UpdateVersion is rejected: catalog versions are immutable.
func (r *StepCatalogRepo) UpdateVersion(context.Context, int, []repo.StepDefDoc) error {
	return repo.ErrCatalogImmutable
}

// DeleteVersion is rejected: catalog versions are immutable.
func (r *StepCatalogRepo) DeleteVersion(context.Context, int) error {
	return repo.ErrCatalogImmutable
}
