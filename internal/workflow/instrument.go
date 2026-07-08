package workflow

import (
	"context"
	"time"

	"onboarding-service/internal/observability"
)

// This is the single place activity-level instrumentation lives: rather than
// pasting counters into each activity, every activity is registered through one
// of these generic wrappers (see Register). Each wrapper records
// onboarding_activity_executions_total{action,status} +
// onboarding_activity_duration_seconds{action} and WARN-logs a failed attempt
// (Temporal retries are separate invocations, so a retry re-enters the wrapper
// and shows as another status=error execution). A nil *Metrics is tolerated so
// tests need not wire metrics.
//
// Two shapes cover every onboarding activity: (ctx,I)->(O,error) and (ctx,I)->error.

// instrumentR wraps an activity that returns a result and an error.
func instrumentR[I, O any](m *observability.Metrics, action string, fn func(context.Context, I) (O, error)) func(context.Context, I) (O, error) {
	return func(ctx context.Context, in I) (O, error) {
		start := time.Now()
		out, err := fn(ctx, in)
		record(ctx, m, action, start, err)
		return out, err
	}
}

// instrumentE wraps an activity that returns only an error.
func instrumentE[I any](m *observability.Metrics, action string, fn func(context.Context, I) error) func(context.Context, I) error {
	return func(ctx context.Context, in I) error {
		start := time.Now()
		err := fn(ctx, in)
		record(ctx, m, action, start, err)
		return err
	}
}

// record emits the activity metrics and WARN-logs a failed attempt.
func record(ctx context.Context, m *observability.Metrics, action string, start time.Time, err error) {
	status := observability.StatusSuccess
	if err != nil {
		status = observability.StatusError
	}
	if m != nil {
		m.ActivityExecutions.WithLabelValues(action, status).Inc()
		m.ActivityDuration.WithLabelValues(action).Observe(time.Since(start).Seconds())
	}
	if err != nil {
		// A failed attempt; Temporal may retry (a fresh invocation re-enters here).
		observability.Log(ctx).Warn("activity attempt failed",
			"action", action, "error", err.Error())
	}
}
