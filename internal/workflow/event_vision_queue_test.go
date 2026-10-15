// Vision routing tests protect the execution budget and legacy command shape.
package workflow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

// TestVisionActivityContext changes only routing and eager execution, never
// validation timeouts/retries or the parent's cancellation inheritance.
func TestVisionActivityContext(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.ExecuteWorkflow(func(ctx sdkworkflow.Context) (sdkworkflow.ActivityOptions, error) {
			return sdkworkflow.GetActivityOptions(visionActivityContext(ctx, isolated)), nil
		})
		require.NoError(t, env.GetWorkflowError())
		var opts sdkworkflow.ActivityOptions
		require.NoError(t, env.GetWorkflowResult(&opts))
		require.Equal(t, 3*time.Minute, opts.StartToCloseTimeout)
		require.Equal(t, time.Minute, opts.HeartbeatTimeout)
		require.Zero(t, opts.ScheduleToStartTimeout)
		require.Zero(t, opts.ScheduleToCloseTimeout)
		require.EqualValues(t, 3, opts.RetryPolicy.MaximumAttempts)
		require.Equal(t, 2*time.Second, opts.RetryPolicy.InitialInterval)
		require.Equal(t, float64(2), opts.RetryPolicy.BackoffCoefficient)
		require.False(t, opts.WaitForCancellation)
		if isolated {
			require.Equal(t, discoverycontract.VisionTaskQueue("default-test-taskqueue"), opts.TaskQueue)
			require.True(t, opts.DisableEagerExecution)
		} else {
			require.Equal(t, "default-test-taskqueue", opts.TaskQueue)
			require.False(t, opts.DisableEagerExecution)
		}
	}
}
