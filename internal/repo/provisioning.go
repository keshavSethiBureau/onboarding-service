package repo

import (
	"context"
	"time"
)

// ProvisioningRecordDoc tracks post-org provisioning per org (LLD §6.1),
// unique-indexed by orgId. Resources maps a provisioning action name
// (e.g. "kong", "svix", "lago", "aws") to the external resource id it produced;
// its presence is the idempotency marker that prevents double-provisioning.
type ProvisioningRecordDoc struct {
	ID        string            `json:"id" bson:"_id,omitempty"`
	OrgID     string            `json:"orgId" bson:"orgId"`
	Status    string            `json:"status" bson:"status"` // "" | completed
	Resources map[string]string `json:"resources" bson:"resources"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt" bson:"updatedAt"`
}

// Provisioning status values.
const ProvisioningStatusCompleted = "completed"

// ProvisioningRecordRepo persists provisioning records. GetByOrgID returns
// (nil, nil) when none exists yet.
type ProvisioningRecordRepo interface {
	GetByOrgID(ctx context.Context, orgID string) (*ProvisioningRecordDoc, error)
	Upsert(ctx context.Context, doc *ProvisioningRecordDoc) error
}
