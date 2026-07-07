package mongo

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	mongoclient "github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	mongoconfig "github.com/Bureau-Inc/bureau-commons-go/mongoclient/config"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"onboarding-service/internal/repo"
)

// catalogTestRepo connects to a local Mongo in an isolated db and creates the
// unique index on version (what makes the max+1 insert-retry race-safe).
func catalogTestRepo(t *testing.T) *StepCatalogRepo {
	t.Helper()
	host := os.Getenv("MONGO_TEST_HOST")
	if host == "" {
		host = "localhost:27017"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := mongoclient.GetOrCreateBureauMongoClient(ctx, &mongoconfig.Config{
		Hosts:          []string{host},
		Database:       "onboarding_catalog_test",
		DisableMetrics: true,
	}, metricx.NewRegistry())
	if err != nil {
		t.Skipf("no MongoDB reachable at %s (%v); skipping catalog integration test", host, err)
	}
	// Fresh collection per test + the unique version index.
	_ = client.GetCollection(repo.CollStepCatalogs).Drop(ctx)
	if _, err := client.GetCollection(repo.CollStepCatalogs).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "version", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_version"),
	}); err != nil {
		t.Fatalf("create unique version index: %v", err)
	}
	t.Cleanup(func() { _ = client.GetDatabase().Drop(context.Background()) })
	return NewStepCatalogRepo(client)
}

var oneStep = []repo.StepDefDoc{{Name: "EMAIL_VERIFIED", Signal: "EMAIL_VERIFIED"}}

// TestCreateVersion_AllocatesMaxPlusOne proves that with max=2 present, creating
// a new version inserts version 3 (max+1), not a count-derived number.
func TestCreateVersion_AllocatesMaxPlusOne(t *testing.T) {
	r := catalogTestRepo(t)
	ctx := context.Background()

	if err := r.EnsureVersion(ctx, 1, oneStep); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := r.EnsureVersion(ctx, 2, oneStep); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	got, err := r.CreateVersion(ctx, oneStep)
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	if got != 3 {
		t.Fatalf("CreateVersion with max=2 = %d, want 3", got)
	}
}

// TestCreateVersion_ConcurrentProducesThreeAndFour proves two concurrent
// creations (max=2) yield exactly {3,4} — one wins 3, the loser retries into 4.
// Never a duplicate, never a skipped assignment derived from count.
func TestCreateVersion_ConcurrentProducesThreeAndFour(t *testing.T) {
	r := catalogTestRepo(t)
	ctx := context.Background()

	if err := r.EnsureVersion(ctx, 1, oneStep); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := r.EnsureVersion(ctx, 2, oneStep); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // release both goroutines together to force the race
			results[idx], errs[idx] = r.CreateVersion(ctx, oneStep)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent CreateVersion[%d]: %v", i, err)
		}
	}
	got := map[int]bool{results[0]: true, results[1]: true}
	if !got[3] || !got[4] || len(got) != 2 {
		t.Fatalf("concurrent versions = %v, want exactly {3,4} (no dup, no skip)", results)
	}

	// The collection now holds exactly versions 1..4, each once.
	docs, err := r.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	seen := map[int]int{}
	for _, d := range docs {
		seen[d.Version]++
	}
	for v := 1; v <= 4; v++ {
		if seen[v] != 1 {
			t.Errorf("version %d present %d times, want exactly 1", v, seen[v])
		}
	}
}

// TestLoadAll_MaxCorrectWhenCountDiffers proves that in a contrived state where
// document count != max (versions 1 and 3 only, no 2), the loaded max is 3.
func TestLoadAll_MaxCorrectWhenCountDiffers(t *testing.T) {
	r := catalogTestRepo(t)
	ctx := context.Background()

	if err := r.EnsureVersion(ctx, 1, oneStep); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := r.EnsureVersion(ctx, 3, oneStep); err != nil {
		t.Fatalf("seed v3: %v", err)
	}

	docs, err := r.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("doc count = %d, want 2", len(docs))
	}
	// max(version) among {1,3} must be 3 (never 2 = count-derived).
	max := 0
	for _, d := range docs {
		if d.Version > max {
			max = d.Version
		}
	}
	if max != 3 {
		t.Fatalf("max version = %d, want 3 (count=2 must not derive latest)", max)
	}

	// And the next CreateVersion allocates 4 (max+1), not 3 (count+1).
	next, err := r.CreateVersion(ctx, oneStep)
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	if next != 4 {
		t.Fatalf("next version = %d, want 4 (max 3 +1, not count 2 +1)", next)
	}
}

// TestUpdateDelete_Rejected proves the collection is insert-only: update and
// delete of an existing version are rejected unconditionally (no Mongo needed —
// the methods reject before touching the client).
func TestUpdateDelete_Rejected(t *testing.T) {
	r := NewStepCatalogRepo(nil)
	if err := r.UpdateVersion(context.Background(), 1, oneStep); !errors.Is(err, repo.ErrCatalogImmutable) {
		t.Errorf("UpdateVersion err = %v, want ErrCatalogImmutable", err)
	}
	if err := r.DeleteVersion(context.Background(), 1); !errors.Is(err, repo.ErrCatalogImmutable) {
		t.Errorf("DeleteVersion err = %v, want ErrCatalogImmutable", err)
	}
}
