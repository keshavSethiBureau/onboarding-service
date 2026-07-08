package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// TestLog_CorrelatesTraceID proves a step-transition log line carries the OTel
// trace_id (+ span_id) so logs join traces, alongside the structured fields.
func TestLog_CorrelatesTraceID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID,
	}))

	// A step-transition-style log (as an activity/controller would emit).
	Log(ctx).Info("step transition",
		"step", "EMAIL_VERIFIED", "status", "completed",
		"userId", "user1", "orgId", "org1", "workflowId", "user1")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log not valid JSON: %v (%q)", err, buf.String())
	}
	if line["trace_id"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("trace_id = %v, want the span's trace id", line["trace_id"])
	}
	if line["span_id"] != "0123456789abcdef" {
		t.Errorf("span_id = %v", line["span_id"])
	}
	for _, f := range []string{"step", "status", "userId", "orgId", "workflowId"} {
		if _, ok := line[f]; !ok {
			t.Errorf("log missing structured field %q", f)
		}
	}
	t.Logf("---- step-transition log line ----\n%s", buf.String())
}

// TestLog_NoSpanNoTraceID: without a span, no trace_id is attached (no crash).
func TestLog_NoSpanNoTraceID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	Log(context.Background()).Info("no span")
	if bytes.Contains(buf.Bytes(), []byte("trace_id")) {
		t.Errorf("unexpected trace_id without a span: %s", buf.String())
	}
}
