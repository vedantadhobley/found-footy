# temporal.md — Go rebuild ledger

**Purpose.** As-shipped state of the Temporal integration —
`internal/infra/temporal/` adapter shape, `cmd/worker/main.go`
registration flow, and workflow-level conventions used by whatever
lives in `internal/workflow/`.

Per-workflow specs (retry policies, signal contracts, spawn patterns)
live in [orchestration.md](./orchestration.md); this doc covers the
substrate.

Cross-refs [`../rebuild-plan.md`](design/rebuild-plan.md) §9
(`internal/infra/temporal`) and §5 (workflow specs). Divergences from
either live in [`../decisions.md`](decisions.md).

**Update rule.** Any change to Client/Worker shape, activity-timeout
policies, or workflow-registration conventions updates this doc in
the same commit.

## Adapter shape

`internal/infra/temporal/` follows the standard adapter template:

```
temporal/
├── client.go            Client struct + NewClient + accessors + StartWorkflow + SignalWorkflow + Close
├── worker.go            Worker struct + NewWorker + Start + Stop
├── instruments.go       RegisterMetrics(reg, log) → *Instruments (counters + histograms)
├── doc.go               package docstring
└── client_test.go       unit tests
```

### `Client` (`client.go`)

Wraps the Temporal SDK's `client.Client` behind our type so we can
inject `*Instruments` at construction, own the `Close()` hook, and
expose a `WorkerShutdownTimeout()` accessor the bootstrap uses for
graceful shutdown ordering.

```go
type Client struct {
    client.Client // embedded — SDK methods promoted onto *Client
    // + unexported: ins *Instruments, namespace, taskQueue, workerShutdownTimeout
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
   [decisions.md 2026-07-07](decisions.md).

2. **Client wraps the SDK type, doesn't return it raw.** Plan wanted
   `NewClient(...) (client.Client, error)`. Shipped returns `*Client`
   with our own methods. **Silent — retroactively defensible.**
   Rationale: allows the WorkerShutdownTimeout accessor for graceful
   shutdown, own the Close hook (with metric emission), later on we
   can add tracing without changing callers.

3. **`SignalWorkflow` method added.** Not in plan §9. Originally for
   AssetPersistenceWorkflow signals — that workflow was superseded
   (collapsed into EventWorkflow), so the method currently has no caller.
   Kept on the adapter (still metric-instrumented) for future signalling.

### `Worker` (`worker.go`)

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
binaries know what they're running (worker vs api vs twitter);
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

// 3. Workflow + activity registration — BEFORE Start. Each Activities
//    struct's exported methods become
//    individually-dispatchable activities). Construction of each *Activities
//    with its real deps is in orchestration.md's wire-up.
w.RegisterWorkflow(ffwf.IngestWorkflow)
w.RegisterWorkflow(ffwf.ActivePollWorkflow)
w.RegisterWorkflow(ffwf.StagingPollWorkflow)
w.RegisterWorkflow(ffwf.EventWorkflow)
w.RegisterWorkflow(ffwf.VideoWorkflow)
w.RegisterActivity(ingestActs)     // *ingest.Activities   (8 methods)
w.RegisterActivity(monitorActs)    // *monitor.Activities
w.RegisterActivity(discoveryActs)  // *discovery.Activities
w.RegisterActivity(videoActs)      // *video.Activities
w.RegisterActivity(visionActs)     // *vision.Activities
w.RegisterActivity(persistActs)    // *video.PersistActivities
w.RegisterActivity(fleetActs)      // *fleet.Activities
w.RegisterActivity(livefeedActs)   // *livefeed.Activities

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

## Workflow-level conventions

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
without depending on the activities package. Used across all the
ingest activities (and the monitor/discovery/video/vision/persist sets).

**Default activity options in each workflow.** No adapter-level
`DefaultRetryPolicy()` helper (plan §9 called for one; not shipped).
Each workflow defines its own `workflow.ActivityOptions` block with
timeout + retry policy inline — visible at the call site, easier to
audit. Divergence from plan; logged in
[decisions.md 2026-07-07](decisions.md).

`ValidateClip` has one error-level exception to the inline numeric retry
policy: the activity returns a non-retryable `vision_llm_permanent`
ApplicationError for invalid JSON/request/auth/model failures. Temporal stops
after the first attempt. Rate-limit, capacity, unavailable, and other transient
failures retain EventWorkflow's three-attempt policy (FF-012).

## Testing shape

Client-level: unit tests exercise `NewClient` connection retries +
`Close` behavior against a stub server. No testcontainers
for Temporal itself (heavy; workspace `temporal` dev container serves
smoke + trigger scripts).

Workflow-level: `testsuite.WorkflowTestSuite` via testify mock —
see [orchestration.md § Testing shape](./orchestration.md#testing-shape)
for the ingest pattern.

## Schedule registration

**IngestWorkflow** — daily 00:05 UTC. Wired in `cmd/worker/main.go`
via `ensureIngestSchedule` (O1e/b). Pattern:

```go
_, err := tempClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
    ID: "ingest-scheduled-daily",
    Spec: client.ScheduleSpec{
        CronExpressions: []string{"5 0 * * *"},
    },
    Action: &client.ScheduleWorkflowAction{
        ID:        "ingest-scheduled",
        Workflow:  ffwf.IngestWorkflow,
        TaskQueue: tempClient.TaskQueue(),
        Args:      []any{ffwf.IngestWorkflowInput{RetentionDays: 14, FetchFuture: true}},
    },
    Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
})
if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
    // Expected on re-startup; treat as success.
}
```

Load-bearing details:
- **Idempotent by design.** ErrScheduleAlreadyRunning is caught +
  logged as `temporal_schedule_already_exists` (not an error). Every
  worker restart hits this after the first successful create.
- **Doesn't overwrite manual updates — but also doesn't propagate CODE
  changes.** Create-only means a changed cron / arg / overlap in this file is
  **silently ignored on redeploy** until the schedule is manually deleted +
  recreated. Python's `setup_schedules()` UPDATEd every startup specifically to
  avoid this (a stale 25s timeout that persisted); reintroduced here — tracked
  as [`FF-009`](./todo.md#confirmed-lower-priority-backlog). (Upside: an operator's manual
  `temporal schedule update` survives a redeploy.)
- **Overlap = SKIP.** If a prior IngestWorkflow run is still
  executing (unusual — ingest is fast, but a Postgres stall could
  cause it), skip the next scheduled run rather than double-firing.

**Three schedules ship** — all via this same idempotent `Create` pattern in
`cmd/worker/main.go` (`ensureIngestSchedule` / `ensureActivePollSchedule` /
`ensureStagingPollSchedule`):

| Schedule ID | Spec | Workflow |
|---|---|---|
| `ingest-scheduled-daily` | cron `5 0 * * *` (00:05 UTC) | IngestWorkflow (`FetchFuture:true, RetentionDays:14`) |
| `active-poll-scheduled` | IntervalSpec `Every: WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL` (30s) | ActivePollWorkflow |
| `staging-poll-scheduled` | cron `WORKFLOWS_STAGING_POLL_CRON` (`*/15 * * * *`) | StagingPollWorkflow |

EventWorkflow + VideoWorkflow are **spawned** (client `StartWorkflow` / child
workflow), not scheduled. EventWorkflow uses its deterministic ID with
`ALLOW_DUPLICATE_FAILED_ONLY`, so running and successful executions remain
singletons while failed, timed-out, canceled, or terminated executions can
resume from Postgres checkpoints. Its client start RPC is bounded, but the
workflow has no arbitrary outer execution timeout; the finite search loop and
per-operation timeouts own the runtime bound. See
[orchestration.md](./orchestration.md).

**Adapter surface:** `Client.ScheduleClient() client.ScheduleClient` —
passthrough to the SDK's ScheduleClient. Not per-op instrumented; schedule ops
are rare.

## Cross-refs

- Plan §9 temporal spec — [rebuild-plan.md § internal/infra/temporal](design/rebuild-plan.md#internalinfratemporal)
- Plan §5 workflow specs — [rebuild-plan.md §5](design/rebuild-plan.md#5-orchestration-layer--temporal-workflows-and-activities)
- Shipped workflow specs — [orchestration.md](./orchestration.md)
- Adapter template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
