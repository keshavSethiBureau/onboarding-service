package workflow

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	sdkclient "go.temporal.io/sdk/client"
)

// dialTemporal connects to a local Temporal (TEMPORAL_TEST_HOSTPORT or
// 127.0.0.1:7233), skipping the test when none is reachable.
func dialTemporal(t *testing.T) sdkclient.Client {
	t.Helper()
	hostPort := os.Getenv("TEMPORAL_TEST_HOSTPORT")
	if hostPort == "" {
		hostPort = "127.0.0.1:7233"
	}
	c, err := sdkclient.Dial(sdkclient.Options{HostPort: hostPort})
	if err != nil {
		t.Skipf("no Temporal reachable at %s (%v); skipping start-vs-signal integration test", hostPort, err)
	}
	// Dial is lazy; force a real round-trip so we skip (not fail) when down.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.CheckHealth(ctx, &sdkclient.CheckHealthRequest{}); err != nil {
		c.Close()
		t.Skipf("Temporal at %s not healthy (%v); skipping", hostPort, err)
	}
	return c
}

func uniqueUserID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestStarter_StartsIfAbsentSignalsIfPresent(t *testing.T) {
	client := dialTemporal(t)
	defer client.Close()

	starter := NewStarter(client, TaskQueue)
	ctx := context.Background()
	userID := uniqueUserID("start-signal")
	t.Cleanup(func() { _ = client.TerminateWorkflow(context.Background(), userID, "", "test cleanup") })

	// First call: no workflow exists -> it is STARTED.
	run1, err := starter.SubmitStep(ctx, userID, "org-1", StepEmailVerified)
	if err != nil {
		t.Fatalf("first SubmitStep: %v", err)
	}
	if run1 == "" {
		t.Fatal("expected a run id from the started workflow")
	}
	// The workflow must exist with WorkflowID = userId.
	desc, err := client.DescribeWorkflowExecution(ctx, userID, "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got := desc.WorkflowExecutionInfo.Execution.WorkflowId; got != userID {
		t.Fatalf("WorkflowID = %q, want %q", got, userID)
	}

	// Second call: workflow already exists -> it is SIGNALLED, same run (no duplicate).
	run2, err := starter.SubmitStep(ctx, userID, "org-1", StepVerticalSelected)
	if err != nil {
		t.Fatalf("second SubmitStep: %v", err)
	}
	if run2 != run1 {
		t.Fatalf("second call started a new run (%q != %q); one-workflow-per-user violated", run2, run1)
	}
}

func TestStarter_ConcurrentCallsSingleWorkflow(t *testing.T) {
	client := dialTemporal(t)
	defer client.Close()

	starter := NewStarter(client, TaskQueue)
	ctx := context.Background()
	userID := uniqueUserID("concurrent")
	t.Cleanup(func() { _ = client.TerminateWorkflow(context.Background(), userID, "", "test cleanup") })

	const n = 12
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		runIDs []string
		errs   []error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runID, err := starter.SubmitStep(ctx, userID, "org-1", StepEmailVerified)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			runIDs = append(runIDs, runID)
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("concurrent SubmitStep errors: %v", errs)
	}
	if len(runIDs) != n {
		t.Fatalf("expected %d run ids, got %d", n, len(runIDs))
	}
	// Every concurrent caller must observe the SAME single workflow run.
	for i, id := range runIDs {
		if id != runIDs[0] {
			t.Fatalf("run id %d = %q differs from %q; concurrent calls created >1 workflow", i, id, runIDs[0])
		}
	}
}
