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

// ProvisioningRecordRepo is the Mongo implementation of repo.ProvisioningRecordRepo.
type ProvisioningRecordRepo struct {
	client *mongoclient.BureauMongoClient
}

// NewProvisioningRecordRepo returns a Mongo-backed provisioning record repository.
func NewProvisioningRecordRepo(client *mongoclient.BureauMongoClient) *ProvisioningRecordRepo {
	return &ProvisioningRecordRepo{client: client}
}

// GetByOrgID returns the provisioning record for an org, or (nil, nil) if none.
func (r *ProvisioningRecordRepo) GetByOrgID(ctx context.Context, orgID string) (*repo.ProvisioningRecordDoc, error) {
	doc, err := mongoclient.FindOneGeneric[repo.ProvisioningRecordDoc](
		r.client, ctx, repo.CollProvisioningRecords, bson.M{"orgId": orgID},
	)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}

// Upsert writes the provisioning record, keyed (and _id'd) by orgId so retries
// never create a duplicate.
func (r *ProvisioningRecordRepo) Upsert(ctx context.Context, doc *repo.ProvisioningRecordDoc) error {
	if doc.ID == "" {
		doc.ID = doc.OrgID
	}
	now := time.Now().UTC()
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	doc.UpdatedAt = now

	result := r.client.ReplaceOne(
		ctx,
		repo.CollProvisioningRecords,
		bson.M{"orgId": doc.OrgID},
		doc,
		options.Replace().SetUpsert(true),
	)
	if !result.Success {
		return result.Error
	}
	return nil
}
