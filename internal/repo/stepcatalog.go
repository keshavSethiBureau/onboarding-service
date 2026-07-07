package repo

import (
	"context"
	"errors"
	"time"
)

// StepDefDoc is one persisted step definition inside a catalog version — the
// storage twin of workflow.StepDef.
type StepDefDoc struct {
	Name          string `json:"name" bson:"name"`
	Action        string `json:"action,omitempty" bson:"action,omitempty"`
	Signal        string `json:"signal,omitempty" bson:"signal,omitempty"`
	MarksComplete bool   `json:"marksComplete,omitempty" bson:"marksComplete,omitempty"`
}

// StepCatalogDoc is one immutable catalog version in step_catalogs. The
// collection is INSERT-ONLY with a unique index on version: a version is never
// updated or deleted — a mistake is corrected by inserting the next version.
type StepCatalogDoc struct {
	ID        string       `json:"id" bson:"_id,omitempty"`
	Version   int          `json:"version" bson:"version"`
	Steps     []StepDefDoc `json:"steps" bson:"steps"`
	CreatedAt time.Time    `json:"createdAt" bson:"createdAt"`
}

// ErrCatalogImmutable is returned for any attempt to update or delete an
// existing catalog version.
var ErrCatalogImmutable = errors.New("step catalog versions are immutable: insert the next version instead of editing")

// ErrCatalogVersionRace is returned when CreateVersion exhausts its retries
// because concurrent creations kept winning the unique-version insert.
var ErrCatalogVersionRace = errors.New("could not allocate a catalog version after retries (concurrent creations)")

// StepCatalogRepo persists catalog versions. Version numbers are ALWAYS derived
// from max(version) — never from a document count, which can diverge from max
// and must never be used to derive a version number.
type StepCatalogRepo interface {
	// CreateVersion inserts a new version = max(version)+1 with the given steps,
	// retrying (bounded) when a concurrent creation wins the unique index.
	CreateVersion(ctx context.Context, steps []StepDefDoc) (int, error)
	// EnsureVersion idempotently inserts a specific known version (startup seed
	// of the deployed baseline). A duplicate-version insert is treated as
	// success — it never updates the existing document.
	EnsureVersion(ctx context.Context, version int, steps []StepDefDoc) error
	// LoadAll returns every catalog version (startup preload).
	LoadAll(ctx context.Context) ([]StepCatalogDoc, error)
	// UpdateVersion always fails with ErrCatalogImmutable.
	UpdateVersion(ctx context.Context, version int, steps []StepDefDoc) error
	// DeleteVersion always fails with ErrCatalogImmutable.
	DeleteVersion(ctx context.Context, version int) error
}
