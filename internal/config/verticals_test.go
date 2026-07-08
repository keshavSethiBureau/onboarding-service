package config

import (
	"testing"

	configlibconfig "github.com/Bureau-Inc/bureau-commons-go/configlib/config"
)

// fakeSource is a minimal verticalSource for tests: it serves canned values and
// captures the hot-reload callback so a test can fire it on demand.
type fakeSource struct {
	values map[string]string
	cb     func(key, oldVal, newVal string)
}

func (f *fakeSource) GetRaw(key string) configlibconfig.ResolvedValue {
	return configlibconfig.ResolvedValue{Value: f.values[key]}
}

func (f *fakeSource) OnKeyChange(_ string, fn func(key, oldVal, newVal string)) {
	f.cb = fn
}

const (
	twoVerticalsJSON = `[{"name":"Fraud","description":"d1","tags":[]},{"name":"KYC","description":"Know Your Customer","tags":["t1"]}]`
	kycQuestionsJSON = `[{"verticalName":"KYC","questions":[{"key":"entity_type","label":"What?","type":"single_choice","options":["Individual","Business"]}]}]`
)

func TestLoadVerticalCache(t *testing.T) {
	src := &fakeSource{values: map[string]string{
		KeyVerticals: twoVerticalsJSON,
		KeyQuestions: kycQuestionsJSON,
	}}

	cache, err := LoadVerticalCache(src)
	if err != nil {
		t.Fatalf("LoadVerticalCache: %v", err)
	}

	if got := cache.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	// lookup by name: present
	kyc, ok := cache.Vertical("KYC")
	if !ok {
		t.Fatal(`Vertical("KYC") not found`)
	}
	if kyc.Description != "Know Your Customer" || len(kyc.Tags) != 1 {
		t.Errorf("KYC vertical = %+v, unexpected fields", kyc)
	}

	// lookup by name: absent
	if _, ok := cache.Vertical("DoesNotExist"); ok {
		t.Error(`Vertical("DoesNotExist") = found, want not found`)
	}

	// questions-per-vertical loaded
	q, ok := cache.Questions("KYC")
	if !ok || len(q.Questions) != 1 || q.Questions[0].Key != "entity_type" {
		t.Errorf("Questions(KYC) = %+v, ok=%v", q, ok)
	}
}

func TestVerticalCacheHotReload(t *testing.T) {
	src := &fakeSource{values: map[string]string{
		KeyVerticals: `[{"name":"Fraud","description":"d","tags":[]}]`,
		KeyQuestions: `[]`,
	}}

	cache, err := LoadVerticalCache(src)
	if err != nil {
		t.Fatalf("LoadVerticalCache: %v", err)
	}
	if cache.Len() != 1 {
		t.Fatalf("initial Len() = %d, want 1", cache.Len())
	}
	if _, ok := cache.Vertical("KYC"); ok {
		t.Fatal("KYC present before reload")
	}

	// Simulate an Apollo push: change the underlying values, then fire the
	// registered hot-reload callback.
	src.values[KeyVerticals] = twoVerticalsJSON
	if src.cb == nil {
		t.Fatal("no hot-reload callback was registered")
	}
	src.cb(KeyVerticals, "", twoVerticalsJSON)

	if cache.Len() != 2 {
		t.Fatalf("after reload Len() = %d, want 2", cache.Len())
	}
	if _, ok := cache.Vertical("KYC"); !ok {
		t.Error("KYC missing after hot-reload")
	}
}

func TestLoadVerticalCacheInvalidJSON(t *testing.T) {
	src := &fakeSource{values: map[string]string{KeyVerticals: "{not-json"}}
	if _, err := LoadVerticalCache(src); err == nil {
		t.Fatal("expected error for invalid verticals JSON, got nil")
	}
}

func TestVerticalCache_MissHandlerFires(t *testing.T) {
	c := NewVerticalCache()
	c.Replace([]Vertical{{Name: "KYC"}}, nil)

	var misses []string
	c.SetMissHandler(func(cache string) { misses = append(misses, cache) })

	if _, ok := c.Vertical("KYC"); !ok {
		t.Fatal("expected hit for KYC")
	}
	if _, ok := c.Vertical("Nope"); ok {
		t.Fatal("expected miss for Nope")
	}
	if _, ok := c.Questions("AlsoNope"); ok {
		t.Fatal("expected miss for AlsoNope questions")
	}
	if len(misses) != 2 || misses[0] != "verticals" || misses[1] != "verticals" {
		t.Fatalf("miss handler fired %v, want two verticals misses", misses)
	}
}

func TestSeedDefaultsParse(t *testing.T) {
	defaults := SeedDefaults()
	verticals, err := parseVerticals(defaults[KeyVerticals])
	if err != nil {
		t.Fatalf("seed verticals do not parse: %v", err)
	}
	if len(verticals) != 4 {
		t.Fatalf("seed verticals = %d, want 4 (Fraud, Credit, KYC, Onboarding)", len(verticals))
	}
	if _, err := parseQuestions(defaults[KeyQuestions]); err != nil {
		t.Fatalf("seed questions do not parse: %v", err)
	}
}
