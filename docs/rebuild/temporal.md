# temporal.md — Go rebuild ledger

**Purpose.** As-shipped state of the Temporal integration —
`internal/infra/temporal/` adapter shape, `cmd/worker/main.go`
registration flow, and workflow-level conventions used by whatever
lives in `internal/workflow/`.

Per-workflow specs (retry policies, signal contracts, spawn patterns)
live in [orchestration.md](./orchestration.md); this doc covers the
substrate.

Cross-refs [`../rebuild-plan.md`](../rebuild-plan.md) §9
(`internal/infra/temporal`) and §5 (workflow specs). Divergences from
either live in [`../decisions.md`](../decisions.md).

**Update rule.** Any change to Client/Worker shape, activity-timeout
policies, or workflow-registration conventions updates this doc in
the same commit.

## Adapter shape (shipped in S5)

`internal/infra/temporal/` follows the standard adapter template:

```
temporal/
├── client.go            Client struct + NewClient + accessors + StartWorkflow + SignalWorkflow + Close
├── worker.go            Worker struct + NewWorker + Start + Stop
├── instruments.go       RegisterMetrics(reg, log) → *Instruments (counters + histograms)
├── doc.go               package docstring
└── client_test.go       unit tests (85 lines)
```

### `Client` (198 lines, `client.go`)

Wraps the Temporal SDK's `client.Client` behind our type so we can
inject `*Instruments` at construction, own the `Close()` hook, and
expose a `WorkerShutdownTimeout()` accessor the bootstrap uses for
graceful shutdown ordering.

```go
type Client struct {
    // wraps go.temporal.io/sdk/client.Client;
    // fields not exported
}

func NewClient(ctx context.Context, cfg config.TemporalConfig, ins *Instruments) (*Client, error)

func (c *Client) Namespace() string
func (c *Client) TaskQueue() string
func (c *Client) WorkerShutdownTimeout() time.Duration

func (c *Client) StartWorkflow(ctx, opts, workflow, args ...) (client.WorkflowRun, error)
func (c *Client) SignalWorkflow(ctx, workflowID, runID, signalName, arg) error
func (c *Client) Close()
```

**Divergences from plan §9 temporal spec:**

1. **`NewClient` takes `*Instruments`, not `*slog.Logger`.** Instruments
   carry logger + metrics + tracing handle together. Consistent with
   every other adapter (S2+); the plan's `logger` param would be an
   outlier. **Silent — but retroactively defensible.** Logged in
   [decisions.md 2026-07-07](../decisions.md).

2. **Client wraps the SDK type, doesn't return it raw.** Plan wanted
   `NewClient(...) (client.Client, error)`. Shipped returns `*Client`
   with our own methods. **Silent — retroactively defensible.**
   Rationale: allows the WorkerShutdownTimeout accessor for graceful
   shutdown, own the Close hook (with metric emission), later on we
   can add tracing without changing callers.

3. **`SignalWorkflow` method added.** Not in plan §9. Needed for
   AssetPersistenceWorkflow signals from downstream workflows.
   Sensible addition; kept.

### `Worker` (99 lines, `worker.go`)

```go
type Worker struct {
    // wraps go.temporal.io/sdk/worker.Worker; fields not exported
}

func NewWorker(c *Client, ins *Instruments, options worker.Options) *Worker
func (w *Worker) RegisterWorkflow(wf any)
func (w *Worker) RegisterActivity(activity any)
func (w *Worker) Start(ctx context.Context) error
func (w *Worker) Stop()
```

`NewWorker` accepts caller-provided `worker.Options` instead of
hardcoding "sensible defaults" as plan §9 suggested. Rationale: cmd
binaries know what they're running (worker vs api vs scaler);
adapter shouldn't decide their concurrency. **Silent — kept.**

`NewWorker` seeds `Options.WorkerStopTimeout` from
`Client.WorkerShutdownTimeout()` if the caller left it zero — one
place to configure graceful-shutdown drain time.

## Registration flow (as-shipped in worker binary)

`cmd/worker/main.go`:

```go
// 1. Adapter construction (in bootstrap Run's closure).
tempIns := temporal.RegisterMetrics(deps.Metrics, deps.Log)
tempClient, err := temporal.NewClient(ctx, deps.Cfg.Temporal, tempIns)
deps.RegisterCloser("temporal-client", func(_ context.Context) error {
    tempClient.Close(); return nil
})

// 2. Worker construction.
w := temporal.NewWorker(tempClient, tempIns, worker.Options{})

// 3. Workflow + activity registration — BEFORE Start.
ingestActs := &ingestactivity.Activities{
    APIFootball: afClient,
    FixtureRepo: pg.NewFixtureRepo(pool),
    AliasRepo:   pg.NewAliasRepo(pool),
}
w.RegisterWorkflow(ffwf.IngestWorkflow)
w.RegisterActivity(ingestActs)

// 4. Start.
if err := w.Start(ctx); err != nil { return err }

// 5. Register closer LAST — LIFO drain runs it FIRST at shutdown.
deps.RegisterCloser("temporal-worker", func(_ context.Context) error {
    w.Stop(); return nil
})
```

**Load-bearing invariants:**

- **Register before Start.** The SDK's reflection walk runs on
  `Start`; anything registered after is silently ignored.
- **Worker closer registered LAST.** The bootstrap's Closer registry
  drains LIFO, so worker.Stop() runs before pg pool.Close() and
  before NATS drain — activities in flight can still use their
  downstream adapters while completing.
- **`WorkerShutdownTimeout` from Client, not baked in.** Env-driven so
  ops can tune drain time without a rebuild.

## Workflow-level conventions (established in O1c)

Not codified as helper functions yet; observed patterns:

**Determinism rules** — from `internal/workflow/ingest.go` docstring:
- Never call `time.Now()` — use `workflow.Now(ctx)`
- Never `fmt.Println` / `log.Print` — use `workflow.GetLogger(ctx)`
- Never spawn goroutines directly — use `workflow.Go`
- Never read env / files / random — all I/O in activities

**Activity dispatch by string name.** Workflows call
`workflow.ExecuteActivity(ctx, "ActivityName", input)` with the
activity's method name as a string. Tradeoff: no compile-time check
on the string, but workflow tests can `env.OnActivity("Name", ...)`
without depending on the activities package. Used across all four
ingest activities.

**Default activity options in each workflow.** No adapter-level
`DefaultRetryPolicy()` helper (plan §9 called for one; not shipped).
Each workflow defines its own `workflow.ActivityOptions` block with
timeout + retry policy inline — visible at the call site, easier to
audit. Divergence from plan; logged in
[decisions.md 2026-07-07](../decisions.md).

## Testing shape

Client-level: unit tests exercise `NewClient` connection retries +
`Close` behavior against a stub server (85 lines). No testcontainers
for Temporal itself (heavy; workspace `temporal` dev container serves
smoke + trigger scripts).

Workflow-level: `testsuite.WorkflowTestSuite` via testify mock —
see [orchestration.md § Testing shape](./orchestration.md#testing-shape)
for the ingest pattern.

## Schedule registration — NOT YET WIRED

Plan §5 W1 says IngestWorkflow runs on schedule `5 0 * * *` (daily
00:05 UTC). MonitorWorkflow says `*/30 * * * * *` (every 30s).
**Neither schedule is registered in `cmd/worker/main.go` today.**
Workflows only fire from manual triggers (`scripts/trigger_ingest`).

Landing schedule registration is an O1e task before MonitorWorkflow
work begins in O2 (needs the same scheduler wiring). Follows the
`client.CreateSchedule` pattern from the Python-era
`archive/src/worker.py` — same shape, Go SDK equivalent.

## Cross-refs

- Plan §9 temporal spec — [rebuild-plan.md § internal/infra/temporal](../rebuild-plan.md#internalinfratemporal)
- Plan §5 workflow specs — [rebuild-plan.md §5](../rebuild-plan.md#5-orchestration-layer--temporal-workflows-and-activities)
- Shipped workflow specs — [orchestration.md](./orchestration.md)
- Adapter template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
