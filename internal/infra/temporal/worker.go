// Worker wrapper — registers workflows/activities and drives the
// task-polling loop. Only the worker binary needs this; api
// uses only the Client.
package temporal

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/worker"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Worker wraps worker.Worker (SDK interface) with lifecycle log
// emissions. Every SDK method (RegisterWorkflow, RegisterActivity,
// RegisterWorkflowWithOptions, ...) is directly callable via the
// embedded interface.
//
// Lifecycle: NewWorker builds the worker; workflows + activities get
// registered via the embedded interface methods; Start begins polling
// on a goroutine and returns immediately; Stop drains in-flight
// activities up to the configured shutdown timeout.
type Worker struct {
	worker.Worker
	client  *Client
	ins     *Instruments
	options worker.Options
}

// NewWorker builds a Temporal worker bound to the client's task queue.
// The worker is NOT running yet — call Start after registering
// workflows + activities.
//
// options carries per-worker tunables (MaxConcurrentActivities,
// MaxConcurrentWorkflowTasks, etc.). Zero-value options is safe; the
// SDK picks reasonable defaults for most fields.
//
// One exception: if options.WorkerStopTimeout is 0, NewWorker seeds it
// from the Client's WorkerShutdownTimeout (populated from
// TemporalConfig at NewClient time — env TEMPORAL_WORKER_SHUTDOWN_TIMEOUT,
// default 60s). The bootstrap Closer registry hardcodes a 10s bounded
// context for every closer, but the SDK's Worker.Stop() ignores that
// context and honors its own WorkerStopTimeout instead — so the
// grace-drain deadline has to live here or long-running video-validation
// activities get force-killed after 10s regardless of config.
func NewWorker(c *Client, ins *Instruments, options worker.Options) *Worker {
	if options.WorkerStopTimeout == 0 && c.WorkerShutdownTimeout() > 0 {
		options.WorkerStopTimeout = c.WorkerShutdownTimeout()
	}
	w := worker.New(c.Client, c.TaskQueue(), options)
	return &Worker{
		Worker:  w,
		client:  c,
		ins:     ins,
		options: options,
	}
}

// Start begins the task-polling loop on a background goroutine. Returns
// immediately; the goroutine runs until Stop is called or an
// unrecoverable error occurs.
//
// The SDK's Worker.Start returns only on failure, so we wrap it in a
// goroutine and translate a return into a worker_failed log emission.
func (w *Worker) Start(ctx context.Context) error {
	if err := w.Worker.Start(); err != nil {
		w.ins.emitEvent(ctx, logging.LevelError,
			vocabulary.ActionTemporalWorkerFailed,
			"temporal worker start failed",
			logging.String("task_queue", w.client.TaskQueue()),
			logging.Err(err),
		)
		return fmt.Errorf("temporal.Worker.Start: %w", err)
	}
	w.ins.emitEvent(ctx, logging.LevelInfo,
		vocabulary.ActionTemporalWorkerStarted,
		"temporal worker started",
		logging.String("task_queue", w.client.TaskQueue()),
		logging.String("namespace", w.client.Namespace()),
	)
	return nil
}

// Stop drains in-flight activities and shuts the worker down. The
// grace-drain deadline is honored by the SDK internally via the
// WorkerStopTimeout option NewWorker set from the Client's
// WorkerShutdownTimeout. Bootstrap's Closer registry ctx doesn't
// participate — the SDK's Stop() blocks until drain completes or its
// own timeout fires.
func (w *Worker) Stop() {
	w.Worker.Stop()
	w.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionTemporalWorkerStopped,
		"temporal worker stopped",
		logging.String("task_queue", w.client.TaskQueue()),
	)
}
