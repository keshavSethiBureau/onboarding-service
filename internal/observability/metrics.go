// Package observability centralises the service's Prometheus instruments
// (commons metricx) and a trace-correlated structured logger (log/slog + OTel).
// Instruments are constructed and registered ONCE here, then injected into the
// components that emit them — so there is a single definition per metric and no
// copy-pasted counters. Label sets are low-cardinality only (step, action,
// route, resource, status, cache) — NEVER userId/orgId (those go in logs).
package observability

import "github.com/Bureau-Inc/bureau-commons-go/metricx"

// Status label values shared across instruments.
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Metrics holds every onboarding_* instrument. Build once with NewMetrics and
// inject; a nil *Metrics is tolerated by all emit sites (metrics simply skipped),
// which keeps tests that don't care about metrics simple.
type Metrics struct {
	// Funnel: one increment per completed step transition (step drop-off).
	StepTransitions *metricx.CounterVec // {step, status}

	// Journey lifecycle.
	WorkflowStarted   metricx.Counter
	WorkflowCompleted metricx.Counter

	// Every activity execution (success/failure/retry visible per action).
	ActivityExecutions *metricx.CounterVec   // {action, status}
	ActivityDuration   *metricx.HistogramVec // {action}

	// Provisioning health (resource = kong|aws|svix|lago).
	ProvisioningTotal    *metricx.CounterVec   // {resource, status}
	ProvisioningDuration *metricx.HistogramVec // {resource}

	// Auth -> Onboarding handoff (internal step-call endpoint).
	InternalStepsReceived *metricx.CounterVec // {step, status}

	// Caches.
	CatalogCacheVersions metricx.Gauge       // number of catalog versions preloaded
	CacheMiss            *metricx.CounterVec // {cache}
}

// NewMetrics constructs and registers all instruments on the shared registry.
func NewMetrics(reg *metricx.Registry) *Metrics {
	m := &Metrics{
		StepTransitions: metricx.NewCounterVec(metricx.CounterOpts{
			Name: "onboarding_step_transitions_total",
			Help: "Onboarding step transitions by step and status (the funnel).",
		}, []string{"step", "status"}),

		WorkflowStarted: metricx.NewCounter(metricx.CounterOpts{
			Name: "onboarding_workflow_started_total",
			Help: "Onboarding journeys started.",
		}),
		WorkflowCompleted: metricx.NewCounter(metricx.CounterOpts{
			Name: "onboarding_workflow_completed_total",
			Help: "Onboarding journeys completed.",
		}),

		ActivityExecutions: metricx.NewCounterVec(metricx.CounterOpts{
			Name: "onboarding_activity_executions_total",
			Help: "Temporal activity executions by action and status (each retry counts).",
		}, []string{"action", "status"}),
		ActivityDuration: metricx.NewHistogramVec(metricx.HistogramOpts{
			Name:    "onboarding_activity_duration_seconds",
			Help:    "Temporal activity execution duration in seconds by action.",
			Buckets: metricx.DefBuckets,
		}, []string{"action"}),

		ProvisioningTotal: metricx.NewCounterVec(metricx.CounterOpts{
			Name: "onboarding_provisioning_total",
			Help: "External provisioning calls by resource and status.",
		}, []string{"resource", "status"}),
		ProvisioningDuration: metricx.NewHistogramVec(metricx.HistogramOpts{
			Name:    "onboarding_provisioning_duration_seconds",
			Help:    "External provisioning call duration in seconds by resource.",
			Buckets: metricx.DefBuckets,
		}, []string{"resource"}),

		InternalStepsReceived: metricx.NewCounterVec(metricx.CounterOpts{
			Name: "onboarding_internal_steps_received_total",
			Help: "Internal step-call requests received (Auth handoff) by step and status.",
		}, []string{"step", "status"}),

		CatalogCacheVersions: metricx.NewGauge(metricx.GaugeOpts{
			Name: "onboarding_catalog_cache_versions",
			Help: "Number of step-catalog versions preloaded into the local cache.",
		}),
		CacheMiss: metricx.NewCounterVec(metricx.CounterOpts{
			Name: "onboarding_cache_miss_total",
			Help: "Cache misses by cache name (catalog|verticals).",
		}, []string{"cache"}),
	}

	metricx.MustRegister(reg,
		m.StepTransitions, m.WorkflowStarted, m.WorkflowCompleted,
		m.ActivityExecutions, m.ActivityDuration,
		m.ProvisioningTotal, m.ProvisioningDuration,
		m.InternalStepsReceived,
		m.CatalogCacheVersions, m.CacheMiss,
	)
	return m
}
