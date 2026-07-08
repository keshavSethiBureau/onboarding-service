package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	configlibconfig "github.com/Bureau-Inc/bureau-commons-go/configlib/config"
)

// Keys under which verticals and questions-per-vertical are stored in Apollo
// (and in the seeded defaults).
const (
	KeyVerticals = "verticals"
	KeyQuestions = "questions"
)

// Vertical is a Bureau product area, stored by name (LLD §5.2). Tags are
// reserved for future use and unused in V1 logic.
type Vertical struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Question is a single questionnaire question for a vertical (display only).
type Question struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Options []string `json:"options"`
}

// VerticalQuestions is the ordered question set for one vertical.
type VerticalQuestions struct {
	VerticalName string     `json:"verticalName"`
	Questions    []Question `json:"questions"`
}

// snapshot is an immutable view of the cache, swapped atomically on reload.
type snapshot struct {
	verticals []Vertical
	byName    map[string]Vertical
	questions map[string]VerticalQuestions
}

// VerticalCache is a per-instance, read-only-at-runtime cache of verticals and
// questions-per-vertical. Reads are lock-free; reloads swap the snapshot
// atomically, so a hot-reload never exposes a partially updated view.
type VerticalCache struct {
	snap   atomic.Pointer[snapshot]
	onMiss func(cache string) // optional; observability hook, set at wiring
}

// NewVerticalCache returns an empty cache.
func NewVerticalCache() *VerticalCache {
	c := &VerticalCache{}
	c.snap.Store(&snapshot{
		byName:    map[string]Vertical{},
		questions: map[string]VerticalQuestions{},
	})
	return c
}

// SetMissHandler installs an optional callback invoked on a lookup miss. Kept as
// a plain func so this package stays decoupled from the metrics package; wiring
// points it at onboarding_cache_miss_total.
func (c *VerticalCache) SetMissHandler(fn func(cache string)) { c.onMiss = fn }

func (c *VerticalCache) reportMiss() {
	if c.onMiss != nil {
		c.onMiss("verticals")
	}
}

// Replace atomically swaps the cache contents.
func (c *VerticalCache) Replace(verticals []Vertical, questions []VerticalQuestions) {
	byName := make(map[string]Vertical, len(verticals))
	for _, v := range verticals {
		byName[v.Name] = v
	}
	byVertical := make(map[string]VerticalQuestions, len(questions))
	for _, q := range questions {
		byVertical[q.VerticalName] = q
	}
	c.snap.Store(&snapshot{verticals: verticals, byName: byName, questions: byVertical})
}

// Verticals returns all verticals in load order.
func (c *VerticalCache) Verticals() []Vertical { return c.snap.Load().verticals }

// Vertical returns the vertical with the given name, if present. A miss reports
// to the observability hook.
func (c *VerticalCache) Vertical(name string) (Vertical, bool) {
	v, ok := c.snap.Load().byName[name]
	if !ok {
		c.reportMiss()
	}
	return v, ok
}

// Questions returns the question set for a vertical, if present. A miss reports
// to the observability hook.
func (c *VerticalCache) Questions(name string) (VerticalQuestions, bool) {
	q, ok := c.snap.Load().questions[name]
	if !ok {
		c.reportMiss()
	}
	return q, ok
}

// Len returns the number of cached verticals (used by the readiness probe).
func (c *VerticalCache) Len() int { return len(c.snap.Load().verticals) }

func parseVerticals(raw string) ([]Vertical, error) {
	var vs []Vertical
	if err := json.Unmarshal([]byte(raw), &vs); err != nil {
		return nil, fmt.Errorf("parse verticals: %w", err)
	}
	return vs, nil
}

func parseQuestions(raw string) ([]VerticalQuestions, error) {
	var qs []VerticalQuestions
	if err := json.Unmarshal([]byte(raw), &qs); err != nil {
		return nil, fmt.Errorf("parse questions: %w", err)
	}
	return qs, nil
}

// verticalSource is the subset of the configlib client the cache depends on.
// It keeps the loader testable without the full BureauConfigClient surface.
type verticalSource interface {
	GetRaw(key string) configlibconfig.ResolvedValue
	OnKeyChange(pattern string, fn func(key, oldVal, newVal string))
}

// LoadVerticalCache builds the cache from the config source and subscribes to
// hot-reload: any change to the verticals/questions keys re-reads and swaps the
// snapshot. The initial load must succeed (verticals must parse).
func LoadVerticalCache(src verticalSource) (*VerticalCache, error) {
	cache := NewVerticalCache()
	if err := reloadVerticalCache(cache, src); err != nil {
		return nil, err
	}
	src.OnKeyChange("^("+KeyVerticals+"|"+KeyQuestions+")$", func(_, _, _ string) {
		if err := reloadVerticalCache(cache, src); err != nil {
			slog.Warn("vertical cache hot-reload failed, keeping previous snapshot", "error", err.Error())
		}
	})
	return cache, nil
}

func reloadVerticalCache(cache *VerticalCache, src verticalSource) error {
	verticals, err := parseVerticals(src.GetRaw(KeyVerticals).Value)
	if err != nil {
		return err
	}
	var questions []VerticalQuestions
	if raw := strings.TrimSpace(src.GetRaw(KeyQuestions).Value); raw != "" {
		if questions, err = parseQuestions(raw); err != nil {
			return err
		}
	}
	cache.Replace(verticals, questions)
	return nil
}

// SeedDefaults returns the configlib Defaults map so the service runs locally
// without an Apollo server. Apollo (when configured) overrides these at runtime.
func SeedDefaults() map[string]string {
	return map[string]string{
		KeyVerticals: defaultVerticalsJSON,
		KeyQuestions: defaultQuestionsJSON,
	}
}

const defaultVerticalsJSON = `[
  {"name":"Fraud","description":"Fraud detection and prevention","tags":[]},
  {"name":"Credit","description":"Credit risk and underwriting","tags":[]},
  {"name":"KYC","description":"Know Your Customer verification","tags":[]},
  {"name":"Onboarding","description":"Customer onboarding journeys","tags":[]}
]`

const defaultQuestionsJSON = `[
  {"verticalName":"KYC","questions":[
    {"key":"entity_type","label":"What are you verifying?","type":"single_choice","options":["Individual","Business"]},
    {"key":"regions","label":"Which regions do you operate in?","type":"multi_choice","options":["India","US","EU","APAC"]}
  ]},
  {"verticalName":"Onboarding","questions":[
    {"key":"monthly_volume","label":"Expected monthly onboarding volume","type":"number","options":[]}
  ]}
]`
