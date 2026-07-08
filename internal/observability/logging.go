package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Log returns the structured logger correlated to the OTel trace in ctx: when a
// span is present its trace_id/span_id are attached so logs join traces. Call
// sites add userId/orgId/workflowId/step/action as fields (fine in logs — never
// as metric labels). Use inside activities, controllers, middleware and wiring —
// NEVER inside workflow code (use workflow.GetLogger there for replay safety).
func Log(ctx context.Context) *slog.Logger {
	logger := slog.Default()
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		logger = logger.With("trace_id", sc.TraceID().String())
		if sc.HasSpanID() {
			logger = logger.With("span_id", sc.SpanID().String())
		}
	}
	return logger
}
