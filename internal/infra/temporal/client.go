// Package temporal is the Temporal workflow-orchestration adapter.
// Wraps go.temporal.io/sdk/client + go.temporal.io/sdk/worker.
//
// Phase S5.1: Client wrapper with StartWorkflow / SignalWorkflow
// instrumentation. Worker wrapper lands in S5.2. Actual workflow +
// activity registration comes in Phase O when domain code is ready.
package temporal

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client wraps client.Client (a Temporal SDK interface — this is an
// interface embed, not a struct embed) with lifecycle log emissions +
// per-StartWorkflow / per-SignalWorkflow metric instrumentation. Same
// role as pg.Pool / nats.Conn / s3.Client.
//
// The wrapped client is the interface value returned by
// client.NewLazyClient. Every underlying method (DescribeWorkflowExecution,
// GetWorkflowHistory, ListWorkflows, ...) remains directly callable via
// the embedded interface.
type Client struct {
	client.Client
	ins       *Instruments
	namespace string
	taskQueue string
}

// NewClient dials the Temporal frontend, verifies connectivity via a
// namespace CheckHealth probe bounded by cfg.ConnectTimeout, and
// returns a Client bound to cfg.Namespace + cfg.TaskQueue.
//
// Callers register cleanup with bootstrap.Deps.RegisterCloser
// ("temporal-client", ...). The client uses a lazy dial internally, so
// Close is more about releasing sockets than draining state.
func NewClient(ctx context.Context, cfg config.TemporalConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("temporal.NewClient: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.HostPort == "" {
		return nil, fmt.Errorf("temporal.NewClient: TEMPORAL_HOSTPORT not set")
	}

	// NewLazyClient defers the actual dial until first RPC — cheaper
	// startup and gives us a clean surface to hit CheckHealth as the
	// explicit health probe.
	c, err := client.NewLazyClient(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionTemporalConnectFailed,
			"temporal client build failed",
			logging.String("hostport", cfg.HostPort),
			logging.Err(err),
		)
		return nil, fmt.Errorf("temporal.NewClient: build client: %w", err)
	}

	// Health probe forces the dial. If Temporal frontend is unreachable
	// this errors within cfg.ConnectTimeout instead of surfacing later
	// on the first StartWorkflow.
	probeCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if _, err := c.CheckHealth(probeCtx, &client.CheckHealthRequest{}); err != nil {
		c.Close()
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionTemporalConnectFailed,
			"temporal health probe failed",
			logging.String("hostport", cfg.HostPort),
			logging.String("namespace", cfg.Namespace),
			logging.Err(err),
		)
		return nil, fmt.Errorf("temporal.NewClient: health probe: %w", err)
	}
	ins.connectionState.Set(1)

	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionTemporalConnected,
		"temporal client ready",
		logging.String("hostport", cfg.HostPort),
		logging.String("namespace", cfg.Namespace),
		logging.String("task_queue", cfg.TaskQueue),
	)

	return &Client{
		Client:    c,
		ins:       ins,
		namespace: cfg.Namespace,
		taskQueue: cfg.TaskQueue,
	}, nil
}

// Namespace returns the namespace this client is bound to. Callers use
// this instead of re-reading config when composing workflow IDs or
// log lines.
func (c *Client) Namespace() string { return c.namespace }

// TaskQueue returns the task queue this client is bound to. Callers use
// this when constructing StartWorkflowOptions where the queue isn't
// otherwise passed in.
func (c *Client) TaskQueue() string { return c.taskQueue }

// StartWorkflow wraps client.ExecuteWorkflow with metric + log
// instrumentation. Same signature as the SDK method; the wrapper adds
// no new required arguments.
//
// workflowType is the string name the workflow was registered under —
// used as a bounded metric label (we have a fixed set: Ingest, Monitor,
// Twitter, Download, Upload, RAG).
func (c *Client) StartWorkflow(
	ctx context.Context,
	options client.StartWorkflowOptions,
	workflowType string,
	args ...interface{},
) (client.WorkflowRun, error) {
	start := time.Now()
	run, err := c.Client.ExecuteWorkflow(ctx, options, workflowType, args...)
	elapsed := time.Since(start)
	c.ins.workflowStartTime.WithLabelValues(workflowType).Observe(elapsed.Seconds())

	if err != nil {
		c.ins.workflowStarts.WithLabelValues(workflowType, "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTemporalStartFailed,
			"temporal start workflow failed",
			logging.String("workflow_type", workflowType),
			logging.String("workflow_id", options.ID),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, err
	}
	c.ins.workflowStarts.WithLabelValues(workflowType, "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionTemporalStartWorkflow,
		"temporal workflow started",
		logging.String("workflow_type", workflowType),
		logging.String("workflow_id", options.ID),
		logging.String("run_id", run.GetRunID()),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return run, nil
}

// SignalWorkflow wraps client.SignalWorkflow with metric + log
// instrumentation.
func (c *Client) SignalWorkflow(
	ctx context.Context,
	workflowID string,
	runID string,
	signalName string,
	arg interface{},
) error {
	err := c.Client.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
	if err != nil {
		c.ins.signals.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTemporalSignalFailed,
			"temporal signal failed",
			logging.String("workflow_id", workflowID),
			logging.String("signal_name", signalName),
			logging.Err(err),
		)
		return err
	}
	c.ins.signals.WithLabelValues("success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionTemporalSignal,
		"temporal signal ok",
		logging.String("workflow_id", workflowID),
		logging.String("signal_name", signalName),
	)
	return nil
}

// Close releases the client's connection pool + emits a lifecycle log
// line. Idempotent — the SDK's Close is safe to call multiple times.
func (c *Client) Close() {
	c.ins.connectionState.Set(0)
	c.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionTemporalClosed,
		"temporal client closed",
	)
	c.Client.Close()
}
