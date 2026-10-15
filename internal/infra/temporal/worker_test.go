// Worker unit tests pin explicit queue identity and graceful-stop configuration.
package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// workerStub replaces only lifecycle calls; embedded registration is unused.
type workerStub struct {
	worker.Worker
	startErr error
	stopped  bool
}

// Start returns the scripted startup result without opening a server.
func (w *workerStub) Start() error { return w.startErr }

// Stop makes successful shutdown observable without SDK pollers.
func (w *workerStub) Stop() { w.stopped = true }

// TestWorkerQueueAndLifecycle records the actual lane and preserves one client.
func TestWorkerQueueAndLifecycle(t *testing.T) {
	sdkClient, err := client.NewLazyClient(client.Options{HostPort: "localhost:1"})
	require.NoError(t, err)
	defer sdkClient.Close()
	c := &Client{Client: sdkClient, taskQueue: "base", namespace: "test", workerShutdownTimeout: time.Minute}
	log := &logging.TestEmitter{}
	ins := RegisterMetrics(metrics.New(), log)
	general := NewWorker(c, ins, worker.Options{})
	require.Equal(t, "base", general.taskQueue)
	vision := NewWorkerForQueue(c, ins, "base-vision", worker.Options{WorkerStopTimeout: 5 * time.Second})
	require.Same(t, c, vision.client)
	require.Equal(t, time.Minute, general.options.WorkerStopTimeout)
	require.Equal(t, 5*time.Second, vision.options.WorkerStopTimeout)
	stub := &workerStub{}
	vision.Worker = stub
	require.NoError(t, vision.Start(context.Background()))
	vision.Stop()
	require.True(t, stub.stopped)
	stub.startErr = errors.New("start failure")
	require.ErrorIs(t, vision.Start(context.Background()), stub.startErr)
	require.Len(t, log.Snapshot(), 3)
	for _, entry := range log.Snapshot() {
		found := false
		for _, field := range entry.Fields {
			if field.Key == "task_queue" {
				require.Equal(t, "base-vision", field.Value)
				found = true
			}
		}
		require.True(t, found)
	}
}
