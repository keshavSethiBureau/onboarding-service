package workflow

import (
	"context"
	"log"
	"time"
)

// StepEvent is the lightweight analytics event emitted on every step
// transition (userId, orgId, stepName, timestamp). WorkflowStarted/Completed are
// set deterministically by the workflow (first step / journey-completed step) so
// the EmitStepEvent activity — not workflow code — increments the journey
// lifecycle counters exactly once each.
type StepEvent struct {
	UserID            string    `json:"userId"`
	OrgID             string    `json:"orgId"`
	Step              string    `json:"step"`
	Timestamp         time.Time `json:"timestamp"`
	WorkflowStarted   bool      `json:"workflowStarted,omitempty"`
	WorkflowCompleted bool      `json:"workflowCompleted,omitempty"`
}

// StepEventSink receives step-transition events. The analytics transport is
// not decided yet (do NOT assume Kafka) — implementations plug in here once
// it is; until then the log-only sink is wired.
type StepEventSink interface {
	Emit(ctx context.Context, evt StepEvent) error
}

// LogStepEventSink is the placeholder sink: it logs the event and succeeds.
type LogStepEventSink struct{}

// Emit logs the step event.
func (LogStepEventSink) Emit(_ context.Context, evt StepEvent) error {
	log.Printf("analytics: step-event user=%s org=%s step=%s at=%s",
		evt.UserID, evt.OrgID, evt.Step, evt.Timestamp.UTC().Format(time.RFC3339))
	return nil
}
