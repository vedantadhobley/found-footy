// provider_health_test.go pins the versioned collector's ordering, total-failure
// path, and fail-closed mutation ordering using the Temporal workflow harness.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	temporalworkflow "go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	ffworkflow "github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestProviderHealthWorkflowRecordsBeforeReconcileAndOnFailure verifies failed
// fetches cannot take an early return that bypasses durable evidence collection.
func TestProviderHealthWorkflowRecordsBeforeReconcileAndOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		fetchFails, collectorFails, legacy bool
	}{
		{"success", false, false, false}, {"all failed", true, false, false}, {"collector failed", false, true, false}, {"legacy history", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterWorkflow(ffworkflow.ActivePollWorkflow)
			env.RegisterActivity(&monitor.Activities{})
			if tc.legacy {
				env.OnGetVersion("provider-health-evidence-v1", temporalworkflow.DefaultVersion, 1).Return(temporalworkflow.DefaultVersion)
			}
			env.OnActivity("GetMonitorConfig", mock.Anything, mock.Anything).Return(monitor.GetMonitorConfigOutput{}, nil).Once()
			env.OnActivity("ActivateUpcoming", mock.Anything, mock.Anything).Return(monitor.ActivateUpcomingOutput{}, nil).Once()
			env.OnActivity("ListActiveFixtureIDs", mock.Anything).Return(monitor.ListActiveFixtureIDsOutput{IDs: []int64{1}}, nil).Once()
			var fetchErr error
			if tc.fetchFails {
				fetchErr = errors.New("fetch unavailable")
			}
			fetch := env.OnActivity("FetchLiveFixtures", mock.Anything, mock.MatchedBy(func(in monitor.FetchLiveFixturesInput) bool {
				return (in.PollID != "") == !tc.legacy && in.ObservedAt.IsZero() == tc.legacy
			})).Return(monitor.FetchLiveFixturesOutput{Fixtures: []apifootball.APIFixture{{Fixture: apifootball.APIFixtureFixture{ID: 1}}}}, fetchErr)
			if tc.fetchFails {
				fetch.Twice()
			} else {
				fetch.Once()
			}
			collected := false
			if !tc.legacy {
				var collectErr error
				if tc.collectorFails {
					collectErr = errors.New("database unavailable")
				}
				call := env.OnActivity("CollectProviderHealth", mock.Anything, mock.MatchedBy(func(in monitor.CollectProviderHealthInput) bool {
					return in.PollID != "" && !in.ObservedAt.IsZero() && in.FetchFailed == tc.fetchFails && len(in.RequestedIDs) == 1
				})).Run(func(mock.Arguments) { collected = true }).Return(providerintegrity.HealthResult{Requested: 1}, collectErr)
				if tc.collectorFails {
					call.Twice()
				} else {
					call.Once()
				}
			}
			if !tc.fetchFails && !tc.collectorFails {
				env.OnActivity("ReconcileFixture", mock.Anything, mock.Anything).Run(func(mock.Arguments) { require.True(t, tc.legacy || collected) }).Return(monitor.ReconcileFixtureOutput{}, nil).Once()
			}
			env.SetTestTimeout(10 * time.Second)
			env.ExecuteWorkflow(ffworkflow.ActivePollWorkflow, ffworkflow.ActivePollWorkflowInput{})
			require.NoError(t, env.GetWorkflowError())
			var out ffworkflow.ActivePollWorkflowOutput
			require.NoError(t, env.GetWorkflowResult(&out))
			if tc.legacy || tc.collectorFails {
				require.Nil(t, out.ProviderHealth)
			} else {
				require.NotNil(t, out.ProviderHealth)
			}
			if tc.collectorFails {
				require.NotEmpty(t, out.Errors)
			}
			env.AssertExpectations(t)
		})
	}
}
