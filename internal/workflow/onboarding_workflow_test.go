package workflow

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// TestOnboardingWorkflow_Completes runs the workflow with the real activity in
// Temporal's in-memory test environment and asserts it completes cleanly.
func TestOnboardingWorkflow_Completes(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(NoOp)

	env.ExecuteWorkflow(OnboardingWorkflow, "user-123")

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow returned error: %v", err)
	}
}

// TestOnboardingWorkflow_InvokesActivity asserts the workflow actually calls the
// NoOp activity exactly once (mocked via the test environment).
func TestOnboardingWorkflow_InvokesActivity(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.OnActivity(NoOp, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(OnboardingWorkflow, "user-abc")

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow returned error: %v", err)
	}
	env.AssertExpectations(t)
}
