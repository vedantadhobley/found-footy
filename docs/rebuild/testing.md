# testing.md — Go rebuild ledger

**Purpose.** As-shipped testing surface — where the ~175 tests live,
which tiers they cover, how each package's test file is structured,
how to run them.

Cross-refs [`../rebuild-plan.md`](../rebuild-plan.md) §12 for the
full testing intent. Divergences from §12 live in
[`../decisions.md`](../decisions.md).

**Update rule.** Every commit that adds a package/adapter/workflow
updates this doc if it introduces a new test tier or pattern.

## Test count by tier (end of Phase O1)

175 tests across the internal/ tree, roughly:

| Tier | Location | Count | Runtime |
|---|---|---|---|
| Unit (pure Go) | domain, activity, config, bootstrap, observability | ~100 | <100ms total |
| Adapter unit (httptest-based) | infra/apifootball, twitter, syndication, wikidata, llm | ~30 | <200ms total |
| Adapter integration (testcontainers) | infra/pg, s3, nats, temporal | ~30 | ~30-60s total |
| Workflow (testsuite.WorkflowTestSuite) | internal/workflow | ~5 (Ingest only, growing) | <100ms per workflow |
| Synthetic e2e | not shipped | 0 | — |

Counts approximate; grep `grep -r "^func Test" internal/ --include="*_test.go" \| wc -l` for the live number.

## Tier 1 — pure Go unit tests

Every domain package + activity package + config + observability
substrate ships a `*_test.go` file with the same shape:

- No adapter imports (fixture/event/video/alias domains verified)
- In-memory fake repos for anything that would touch a database
- Table-driven where the case matrix is enum-shaped
- Runtime <10ms per package

Example — `internal/activity/ingest/activities_test.go` uses in-memory
`fakeFixtureRepo`, `fakeAliasRepo`, `fakeFetcher` — all defined in the
same test file (not `internal/testutil/`, per the "build fakes when
sharing surfaces" rule; see [architecture.md](./architecture.md#as-shipped-tree)).

## Tier 1.5 — workflow tests

`internal/workflow/ingest_test.go` uses
`testsuite.WorkflowTestSuite` from `go.temporal.io/sdk`. Pattern:

1. `env := newEnv(&s)` — creates a `TestWorkflowEnvironment`,
   registers the workflow + a zero-value `&ingest.Activities{}` so
   `OnActivity` can find them.
2. `env.OnActivity("ActivityName", mock.Anything, mock.Anything).Return(...)`
   per activity — testify mock intercepts by name.
3. `env.ExecuteWorkflow(workflow.X, input)` — runs synchronously.
4. `env.IsWorkflowCompleted()`, `GetWorkflowError()`,
   `GetWorkflowResult(&out)`, `AssertExpectations(t)`.

`AssertExpectations` catches both "expected activity fired" AND
"unexpected activity did NOT fire" — the mechanism protecting the
conditional-skip branches (empty TeamRefs skips alias step; zero
RetentionThreshold skips prune step) in IngestWorkflow.

## Tier 2 — adapter integration (testcontainers)

Four adapters use testcontainers-go: `pg`, `s3`, `nats`, `temporal`.
Each spins its real container in the test process.

Enablement mechanics:
- `--network=host` on the `test` make target — testcontainers-go
  connects to the containers by their exposed ports on host
- `/var/run/docker.sock` mounted into the test container so the
  test harness can start sibling containers
- Skip with `-short` (all testcontainers tests check `testing.Short()`
  at the top and skip; `make test-short` completes in <5s across all
  packages)

Example — `internal/infra/pg/fixture_repo_test.go`:

```go
func TestFixtureRepo_Upsert_RoundTrip(t *testing.T) {
    if testing.Short() { t.Skip("integration") }
    pool, cleanup := testPGPool(t)
    defer cleanup()
    repo := pg.NewFixtureRepo(pool)
    // ...
}
```

`testPGPool` (test helper) spins a Postgres container with
`WithInitScripts(...)` pointing at `internal/infra/pg/schema.sql` —
the same schema file dev postgres mounts via
docker-entrypoint-initdb.d. Provides confidence that dev + test + prod
DDL are the same source.

## Tier 3 — synthetic e2e — SHIPPED (Phase 1a)

YAML scenario harness lives at [`test/`](../../test/) with
scenarios under `test/scenarios/<suite>/<name>.yaml`. Design in
[`proposals/test-corpus.md`](./proposals/test-corpus.md).

**How it works** (recap):
- One testcontainer Postgres per test binary run (shared across
  scenarios via TRUNCATE between)
- One httptest.Server mocking api-sports.io (reconfigured per scenario
  via SetResponses)
- One `testsuite.WorkflowTestSuite` per scenario (in-memory Temporal)
- Real workflow + activity code executed against the real pg
- Activity clock injected from scenario's `manual_date` for
  determinism

**Suites** (each a subdirectory under `test/scenarios/`):
- `basic/` — happy paths, sanity checks
- `debounce/` — symmetric counter behavior (increment, decrement, cap,
  floor, threshold flip)
- `faults/` — API 500, timeout, rate-limit
- `edge_cases/` — postponed, own goals, late-game
- `regression/` — scenarios born from prod bugs (link each YAML to
  the git commit or Loki query that surfaced it)

**Currently shipped scenarios (8, all passing in 2.75s):**

basic/ (5):
- `ingest_happy_path` — daily ingest, 2 staging fixtures
- `ingest_manual_ids` — ManualFixtureIDs re-ingest path
- `ingest_terminal_at_seed` — API returns FT → completed at ingest
- `ingest_activate_at_seed` — kickoff within 30-min window → activate
- `ingest_existing_preserves_state` — merge preserves activated_at

debounce/ (3):
- `var_overturn` — full 6-cycle debounce: 3 present cycles trigger
  downstream, 3 absent cycles hit-zero → soft-delete with reason=var
- `flicker_no_reset` — 7-cycle oscillation demonstrating symmetric
  counter (NOT Python's hard reset). Ends at count=3, not removed.
- `threshold_flip` — 8 consecutive present cycles verify
  downstream_triggered flips exactly once + counter caps at 3

**Runtime:** ~3s per scenario (dominated by testcontainer boot).
Amortized when running the full corpus: ~2s once, then ~40ms per
scenario after. Target for full corpus of 50 scenarios: <90s.

**Run:** `make test-corpus` — runs just the harness. `make test`
includes it. `make test-short` excludes it (no docker).

The live smoke-test scripts in `scripts/` provide different coverage
(real api-sports.io + real Temporal + dev pg):
- `scripts/smoke_repos/main.go` — dev pg + repo roundtrip
- `scripts/trigger_ingest/main.go` — dev end-to-end IngestWorkflow
  against real api-sports.io + pg (~1 API request per run)

## Running tests

Three make targets, all Docker-mounted (nothing on host):

```
make test-short   # ~5s — pure-Go + httptest tiers only, no containers
make test         # ~60s — everything including testcontainers
make test <pkg>   # <10s — single package
```

The `test-short` target is the pre-commit gate (fast, catches most
regressions). The `test` target is the pre-push gate (slow, exercises
real integrations).

## Coverage floors — NOT ENFORCED

Plan §12 called for per-package coverage floors as part of the CI
gate. Not implemented. Currently only "does it compile + do tests
pass" is enforced. **Not blocking; low priority until we regress on
coverage in practice.**

## Cross-refs

- Plan §12 — [rebuild-plan.md §12](../rebuild-plan.md#12-testing)
- Adapter test template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
- Workflow test pattern — [orchestration.md § Testing shape](./orchestration.md#testing-shape)
- Live smoke scripts — [`scripts/`](../../scripts/)
