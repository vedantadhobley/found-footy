// Isolated real-Temporal tests prove queue waiting, cancellation, and recovery.
package worker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/config"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// laneProbeWorkflow uses short execution budgets to prove real queue semantics
// without spending three minutes per case or calling any model/provider.
func laneProbeWorkflow(ctx workflow.Context, name string) (string, error) {
	opts := workflow.ActivityOptions{StartToCloseTimeout: time.Second,
		RetryPolicy: &sdktemporal.RetryPolicy{MaximumAttempts: 1}}
	activityName := "RegularProbe"
	if name != "general" {
		opts.TaskQueue = discoverycontract.VisionTaskQueue(workflow.GetInfo(ctx).TaskQueueName)
		opts.DisableEagerExecution = true
		activityName = "VisionProbe"
	}
	if name == "holder" {
		opts.StartToCloseTimeout = 20 * time.Second
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var out string
	err := workflow.ExecuteActivity(ctx, activityName, name).Get(ctx, &out)
	return out, err
}

// TestVisionQueueDurableAdmission proves capped pollers leave excess tasks
// scheduled beyond their execution timeout, without starving general work.
// Cancellation prevents a queued call from reaching the fake inference body;
// replacing the vision poller preserves scheduled work and its first attempt.
func TestVisionQueueDurableAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("real Temporal integration requires Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "temporalio/temporal:1.5.1",
			ExposedPorts: []string{"7233/tcp"},
			Cmd:          []string{"server", "start-dev", "--headless", "--ip", "0.0.0.0", "--dynamic-config-value", "frontend.enableServerVersionCheck=false"},
			Env:          map[string]string{"GOMEMLIMIT": "384MiB", "GOMAXPROCS": "2"},
			HostConfigModifier: func(h *container.HostConfig) {
				h.Memory, h.MemorySwap = 512<<20, 512<<20
			},
			WaitingFor: wait.ForListeningPort("7233/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(server)) })
	endpoint, err := server.Endpoint(ctx, "")
	require.NoError(t, err)
	ins := temporal.RegisterMetrics(metrics.New(), &logging.TestEmitter{})
	c, err := temporal.NewClient(ctx, config.TemporalConfig{HostPort: endpoint, Namespace: "default",
		TaskQueue: "queue-test", ConnectTimeout: 10 * time.Second, WorkerShutdownTimeout: time.Second}, ins)
	require.NoError(t, err)
	t.Cleanup(c.Close)

	release, entered := make(chan struct{}), make(chan struct{}, 2)
	var active, maximum atomic.Int32
	var canceledRan atomic.Bool
	probe := func(ctx context.Context, name string) (string, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		info := activity.GetInfo(ctx)
		if info.TaskQueue != discoverycontract.VisionTaskQueue(c.TaskQueue()) || info.Attempt != 1 {
			return "", fmt.Errorf("unexpected queue/attempt: %s/%d", info.TaskQueue, info.Attempt)
		}
		if name == "cancel" {
			canceledRan.Store(true)
		}
		if name == "holder" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return name, nil
	}
	general := temporal.NewWorker(c, ins, worker.Options{MaxConcurrentActivityExecutionSize: 1})
	general.RegisterWorkflow(laneProbeWorkflow)
	general.RegisterActivityWithOptions(func(_ context.Context, name string) (string, error) { return name, nil }, activity.RegisterOptions{Name: "RegularProbe"})
	// Retained legacy registration must not allow eager execution to bypass
	// the new queue's slots. The probe rejects any execution on the base queue.
	general.RegisterActivityWithOptions(probe, activity.RegisterOptions{Name: "VisionProbe"})
	require.NoError(t, general.Start(ctx))
	t.Cleanup(general.Stop)
	newVisionWorker := func() *temporal.Worker {
		w := temporal.NewWorkerForQueue(c, ins, discoverycontract.VisionTaskQueue(c.TaskQueue()), visionWorkerOptions(2))
		w.RegisterActivityWithOptions(probe, activity.RegisterOptions{Name: "VisionProbe"})
		return w
	}
	vision := newVisionWorker()
	require.NoError(t, vision.Start(ctx))
	visionRunning := true
	t.Cleanup(func() {
		if visionRunning {
			vision.Stop()
		}
	})
	start := func(name string) client.WorkflowRun {
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: c.TaskQueue()}, laneProbeWorkflow, name)
		require.NoError(t, err)
		return run
	}
	get := func(run client.WorkflowRun, want string) {
		var result string
		require.NoError(t, run.Get(ctx, &result))
		require.Equal(t, want, result)
	}
	holders := []client.WorkflowRun{start("holder"), start("holder")}
	for range holders {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("vision execution slots did not fill")
		}
	}
	followers := make([]client.WorkflowRun, 4)
	for i := range followers {
		followers[i] = start("queued")
		requireScheduled(t, ctx, c, followers[i])
	}
	canceled := start("cancel")
	requireScheduled(t, ctx, c, canceled)
	require.NoError(t, c.CancelWorkflow(ctx, canceled.GetID(), canceled.GetRunID()))
	require.True(t, sdktemporal.IsCanceledError(canceled.Get(ctx, nil)))
	get(start("general"), "general")
	// Longer than the followers' StartToClose budget. Their attempts must
	// remain scheduled rather than timing out or consuming a retry.
	time.Sleep(1200 * time.Millisecond)
	for _, run := range followers {
		requireScheduled(t, ctx, c, run)
	}
	close(release)
	for _, run := range holders {
		get(run, "holder")
	}
	for _, run := range followers {
		get(run, "queued")
	}
	require.EqualValues(t, 2, maximum.Load())
	require.False(t, canceledRan.Load())

	// Unpolled tasks survive poller replacement, not just local saturation.
	vision.Stop()
	visionRunning = false
	restarted := start("restart")
	requireScheduled(t, ctx, c, restarted)
	time.Sleep(1200 * time.Millisecond)
	requireScheduled(t, ctx, c, restarted)
	vision = newVisionWorker()
	require.NoError(t, vision.Start(ctx))
	visionRunning = true
	get(restarted, "restart")
}

// requireScheduled waits for the server's durable activity record and rejects
// already-started or retried work. It never connects to the production namespace.
func requireScheduled(t *testing.T, ctx context.Context, c *temporal.Client, run client.WorkflowRun) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := c.DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
		return err == nil && len(out.PendingActivities) == 1 &&
			out.PendingActivities[0].State == enums.PENDING_ACTIVITY_STATE_SCHEDULED &&
			out.PendingActivities[0].Attempt == 1
	}, 5*time.Second, 20*time.Millisecond)
}
