// Tests for the scheduled Twitter maintenance workflow.
package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	twittermaintenance "github.com/vedantadhobley/found-footy/internal/activity/twittermaintenance"
)

func TestTwitterMaintenanceWorkflowUsesSafeDefaults(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(TwitterMaintenanceWorkflow)
	env.RegisterActivity(&twittermaintenance.Activities{})
	env.OnActivity("RunTwitterMaintenance", mock.Anything, mock.MatchedBy(func(in twittermaintenance.RunTwitterMaintenanceInput) bool {
		return in.Query == defaultTwitterCanaryQuery &&
			in.MaxAgeMinutes == defaultTwitterCanaryMaxAge &&
			in.MinTweets == defaultTwitterCanaryMinTweets &&
			in.MinVideos == defaultTwitterCanaryMinVideos
	})).Return(func(context.Context, twittermaintenance.RunTwitterMaintenanceInput) (twittermaintenance.RunTwitterMaintenanceOutput, error) {
		return twittermaintenance.RunTwitterMaintenanceOutput{StopReason: "age", TweetsParsed: 4}, nil
	})

	env.ExecuteWorkflow(TwitterMaintenanceWorkflow, TwitterMaintenanceWorkflowInput{})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}
