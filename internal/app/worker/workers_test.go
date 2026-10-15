// Worker lifecycle tests protect capped admission and one shared drain window.
package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lifecycleStub makes startup order and overlapping drain observable without RPC.
type lifecycleStub struct {
	start func() error
	stop  func()
}

// Start implements the narrow composition-root lifecycle boundary.
func (w lifecycleStub) Start(context.Context) error { return w.start() }

// Stop implements the narrow composition-root lifecycle boundary.
func (w lifecycleStub) Stop() { w.stop() }

// TestVisionWorkerOptions ties execution slots to the existing chat semaphore.
func TestVisionWorkerOptions(t *testing.T) {
	for _, cap := range []int{1, 2, 4} {
		opts := visionWorkerOptions(cap)
		require.True(t, opts.DisableWorkflowWorker)
		require.Equal(t, cap, opts.MaxConcurrentActivityExecutionSize)
		require.Zero(t, opts.TaskQueueActivitiesPerSecond, "admission is slot-based, not a fixed-rate delay")
	}
}

// TestStartWorkersFailureUnwindsOnlyStarted stops the first lane when the
// second fails, without double-stopping a worker that never started.
func TestStartWorkersFailureUnwindsOnlyStarted(t *testing.T) {
	want := errors.New("start failure")
	var firstStopped, secondStopped atomic.Bool
	first := lifecycleStub{start: func() error { return nil }, stop: func() { firstStopped.Store(true) }}
	second := lifecycleStub{start: func() error { return want }, stop: func() { secondStopped.Store(true) }}
	require.ErrorIs(t, startWorkers(context.Background(), first, second), want)
	require.True(t, firstStopped.Load())
	require.False(t, secondStopped.Load())
}

// TestStopWorkersDrainsConcurrently requires both stops to enter before either
// may finish, so a serial implementation fails instead of doubling the grace.
func TestStopWorkersDrainsConcurrently(t *testing.T) {
	entered, release, done := make(chan struct{}, 2), make(chan struct{}), make(chan struct{})
	w := lifecycleStub{stop: func() { entered <- struct{}{}; <-release }}
	go func() { stopWorkers(w, w); close(done) }()
	defer func() { close(release); <-done }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker drains did not overlap")
		}
	}
	select {
	case <-done:
		t.Fatal("dependencies could close before worker drains finished")
	default:
	}
}
