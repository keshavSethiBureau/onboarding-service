package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// parseLevel maps a config string to a slog.Level. Unknown/empty => info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewJSONHandler builds a structured JSON slog handler at the given level. The
// writer is a parameter so tests can capture output.
func NewJSONHandler(w io.Writer, level string) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
}

// InitLogger installs a JSON logger at the given level as the process-wide slog
// default. Call once at startup, before any other logging. All subsequent
// observability.Log(ctx) and slog.* calls emit structured, leveled JSON.
func InitLogger(level string) {
	slog.SetDefault(slog.New(NewJSONHandler(os.Stderr, level)))
}

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
