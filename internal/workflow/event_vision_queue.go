// Vision routing keeps capacity waits outside a running activity's deadline.
package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

const visionTaskQueueChangeID = "ff-037-vision-task-queue"

// visionActivityContext leaves queue waiting unbounded by an activity timeout:
// the three-minute StartToClose budget begins only after a capped worker polls
// the task. Parent cancellation still cancels queued work. Old histories retain
// their original default queue and command attributes.
func visionActivityContext(ctx workflow.Context, isolated bool) workflow.Context {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumAttempts: 3},
	}
	if isolated {
		opts.TaskQueue = discoverycontract.VisionTaskQueue(workflow.GetInfo(ctx).TaskQueueName)
		// Explicitly forbid workflow-worker eager execution from bypassing the
		// dedicated activity worker's slots, even if registrations change later.
		opts.DisableEagerExecution = true
	}
	return workflow.WithActivityOptions(ctx, opts)
}
