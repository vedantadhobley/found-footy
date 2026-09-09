// staging_poll_reap_test.go covers bounded cleanup retries and vendor-poll failure isolation.
package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	fleetactivity "github.com/vedantadhobley/found-footy/internal/activity/fleet"
	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/workflow"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"
)

// TestStagingPoll_ReaperRetries preserves old histories and bounds new failure recovery.
func TestStagingPoll_ReaperRetries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     sdkworkflow.Version
		failures    int
		attempts    int
		vendorFails bool
		wantErrors  int
	}{
		{name: "transient cleanup", version: 1, failures: 1, attempts: 2},
		{name: "exhausted cleanup", version: 1, failures: 10, attempts: 3, wantErrors: 1},
		{name: "legacy cleanup", version: sdkworkflow.DefaultVersion, failures: 10, attempts: 1, wantErrors: 1},
		{name: "vendor failure still cleans", version: 1, attempts: 1, vendorFails: true, wantErrors: 1},
		{name: "legacy vendor failure", version: sdkworkflow.DefaultVersion, vendorFails: true, wantErrors: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := newStagingPollEnv(&suite)
			env.OnGetVersion("ff-073-fleet-reaper", sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(tc.version).Maybe()
			var pollErr error
			pollCalls := 1
			if tc.vendorFails {
				pollErr, pollCalls = errors.New("vendor unavailable"), 2
			}
			env.OnActivity("PollStagingFixtures", mock.Anything, mock.Anything).
				Return(monitor.PollStagingFixturesOutput{}, pollErr).Times(pollCalls)
			attempts := 0
			if tc.attempts > 0 {
				env.OnActivity("ReapOrphanedFirefox", mock.Anything, fleetactivity.ReapOrphanedFirefoxInput{MinAgeSecs: 120}).
					Return(func(context.Context, fleetactivity.ReapOrphanedFirefoxInput) (fleetactivity.ReapOrphanedFirefoxOutput, error) {
						attempts++
						if attempts <= tc.failures {
							return fleetactivity.ReapOrphanedFirefoxOutput{}, errors.New("Docker removal failed")
						}
						return fleetactivity.ReapOrphanedFirefoxOutput{Reaped: []string{"owned-browser"}}, nil
					}).Times(tc.attempts)
			}
			env.ExecuteWorkflow(workflow.StagingPollWorkflow, workflow.StagingPollWorkflowInput{})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var out workflow.StagingPollWorkflowOutput
			if err := env.GetWorkflowResult(&out); err != nil {
				t.Fatal(err)
			}
			if attempts != tc.attempts || len(out.Errors) != tc.wantErrors {
				t.Errorf("attempts=%d errors=%v; want %d, %d", attempts, out.Errors, tc.attempts, tc.wantErrors)
			}
			if tc.failures >= tc.attempts && tc.attempts > 0 && (len(out.Errors) == 0 || !strings.Contains(out.Errors[len(out.Errors)-1], "Docker removal failed")) {
				t.Error("exhausted cleanup lost its cause")
			}
			env.AssertExpectations(t)
		})
	}
}
