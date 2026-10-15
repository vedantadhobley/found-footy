// Worker lifecycle helpers keep independent queues inside one process drain.
package worker

import (
	"context"
	"sync"

	"go.temporal.io/sdk/worker"
)

// temporalWorker is the lifecycle boundary shared by the general and vision
// pollers. Registration remains concrete in the composition root.
type temporalWorker interface {
	Start(context.Context) error
	Stop()
}

// visionWorkerOptions reserves execution capacity before polling validation,
// rather than accepting a burst that waits inside the LLM semaphore. The same
// process-local chat cap remains the upper bound; Control owns global capacity.
func visionWorkerOptions(chatCap int) worker.Options {
	return worker.Options{
		DisableWorkflowWorker:              true,
		MaxConcurrentActivityExecutionSize: chatCap,
	}
}

// startWorkers unwinds every successfully started poller on partial startup.
func startWorkers(ctx context.Context, workers ...temporalWorker) error {
	for i, w := range workers {
		if err := w.Start(ctx); err != nil {
			stopWorkers(workers[:i]...)
			return err
		}
	}
	return nil
}

// stopWorkers drains queues concurrently under their own SDK grace periods.
// Serial stops would multiply the shutdown budget and risk Compose killing the
// second worker before its activities finish. Dependencies close afterward.
func stopWorkers(workers ...temporalWorker) {
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Go(w.Stop)
	}
	wg.Wait()
}
