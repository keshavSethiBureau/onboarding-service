package workflow

import (
	"fmt"
	"sort"

	"onboarding-service/internal/repo"
)

// CatalogCache is an immutable, preloaded snapshot of the step catalog versions
// an instance can execute. It is built once at startup (from the durable
// step_catalogs collection) and never mutated afterwards, so both the executor
// (via CatalogSteps) and the Starter (via LatestVersion) read a stable,
// replay-safe view for the instance's lifetime.
type CatalogCache struct {
	versions map[int][]StepDef
}

// NewCatalogCache builds a cache from an in-code version map (defensively
// copied). Used for the built-in default and in tests.
func NewCatalogCache(versions map[int][]StepDef) *CatalogCache {
	cp := make(map[int][]StepDef, len(versions))
	for v, steps := range versions {
		cp[v] = steps
	}
	return &CatalogCache{versions: cp}
}

// CacheFromDocs builds a cache from the durable catalog documents (startup
// preload).
func CacheFromDocs(docs []repo.StepCatalogDoc) *CatalogCache {
	versions := make(map[int][]StepDef, len(docs))
	for _, d := range docs {
		steps := make([]StepDef, len(d.Steps))
		for i, s := range d.Steps {
			steps[i] = StepDef{Name: s.Name, Action: s.Action, Signal: s.Signal, MarksComplete: s.MarksComplete}
		}
		versions[d.Version] = steps
	}
	return &CatalogCache{versions: versions}
}

// LatestVersion returns max(version) among the cached versions, or 0 if empty.
// It is derived from the version KEYS — deliberately never a document count,
// which can diverge from the true max under races or anomalies.
func (c *CatalogCache) LatestVersion() int {
	latest := 0
	for v := range c.versions {
		if v > latest {
			latest = v
		}
	}
	return latest
}

// Steps returns the ordered step list for a version (nil if unknown).
func (c *CatalogCache) Steps(version int) []StepDef { return c.versions[version] }

// Versions returns the cached version numbers ascending.
func (c *CatalogCache) Versions() []int {
	out := make([]int, 0, len(c.versions))
	for v := range c.versions {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// ValidateActions returns an error if any action referenced by any cached
// version lacks a registered activity handler. Called at startup so an
// instance that has a catalog version it cannot execute fails fast (readiness)
// rather than stranding journeys pinned to that version.
func (c *CatalogCache) ValidateActions(registered map[string]bool) error {
	for _, v := range c.Versions() {
		for _, step := range c.versions[v] {
			if step.Action == "" {
				continue
			}
			if !registered[step.Action] {
				return fmt.Errorf(
					"catalog version %d step %q references action %q with no registered activity handler",
					v, step.Name, step.Action)
			}
		}
	}
	return nil
}

// RegisteredActions is the set of activity handler names the executor can
// dispatch a catalog Action to. It MUST stay in sync with Register — startup
// validation checks every catalog action against this set.
func RegisteredActions() map[string]bool {
	return map[string]bool{
		ActionCreateOrganisation:   true,
		ActionProvisionKong:        true,
		ActionProvisionAWS:         true,
		ActionProvisionSvix:        true,
		ActionProvisionLago:        true,
		ActionCompleteProvisioning: true,
	}
}

// StepDefsToDocs converts in-code step defs to their storage twins (startup
// seed of the deployed baseline into step_catalogs).
func StepDefsToDocs(steps []StepDef) []repo.StepDefDoc {
	docs := make([]repo.StepDefDoc, len(steps))
	for i, s := range steps {
		docs[i] = repo.StepDefDoc{Name: s.Name, Action: s.Action, Signal: s.Signal, MarksComplete: s.MarksComplete}
	}
	return docs
}

// activeCatalog is the catalog the executor and Starter read. It defaults to
// the in-code catalog and is replaced once at startup by UseCatalogCache after
// the durable versions are preloaded and validated.
var activeCatalog = NewCatalogCache(stepCatalog)

// UseCatalogCache installs the preloaded catalog. Call once at startup, before
// the worker starts polling — never while workflows are executing.
func UseCatalogCache(c *CatalogCache) { activeCatalog = c }

// BuiltinCatalog exposes the in-code catalog for startup seeding.
func BuiltinCatalog() map[int][]StepDef { return stepCatalog }
