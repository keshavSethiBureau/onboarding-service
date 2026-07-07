package workflow

import (
	"testing"

	"onboarding-service/internal/repo"
)

// TestLatestVersion_IsMaxNotCount proves LatestVersion returns max(version),
// and stays correct in a contrived state where the document COUNT differs from
// the max: versions {1, 3} present (count=2) must resolve latest=3, never 2.
func TestLatestVersion_IsMaxNotCount(t *testing.T) {
	cache := CacheFromDocs([]repo.StepCatalogDoc{
		{Version: 1, Steps: []repo.StepDefDoc{{Name: "A"}}},
		{Version: 3, Steps: []repo.StepDefDoc{{Name: "A"}}},
	})

	if got := cache.LatestVersion(); got != 3 {
		t.Fatalf("LatestVersion() = %d, want 3 (max, not count=2)", got)
	}
	if got := len(cache.Versions()); got != 2 {
		t.Fatalf("Versions() count = %d, want 2 — confirms count(2) != max(3)", got)
	}
}

func TestLatestVersion_EmptyCacheIsZero(t *testing.T) {
	if got := CacheFromDocs(nil).LatestVersion(); got != 0 {
		t.Fatalf("LatestVersion() on empty cache = %d, want 0", got)
	}
}

// TestValidateActions proves startup validation flags a catalog version that
// references an action with no registered handler, and passes the built-in one.
func TestValidateActions(t *testing.T) {
	if err := NewCatalogCache(BuiltinCatalog()).ValidateActions(RegisteredActions()); err != nil {
		t.Fatalf("built-in catalog should validate against registered actions: %v", err)
	}

	bad := CacheFromDocs([]repo.StepCatalogDoc{
		{Version: 2, Steps: []repo.StepDefDoc{
			{Name: "STEP_X", Action: "NotARegisteredHandler"},
		}},
	})
	if err := bad.ValidateActions(RegisteredActions()); err == nil {
		t.Fatal("expected validation error for an unregistered action handler")
	}
}
