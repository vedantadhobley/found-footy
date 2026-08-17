# testing.md — Go rebuild ledger

**Purpose.** As-shipped testing surface — which tiers exist, how each package's
tests are structured, and how to run them. Counts are derived from the tree,
not copied into this ledger.

Cross-refs [`../rebuild-plan.md`](design/rebuild-plan.md) §12 for the
full testing intent. Divergences from §12 live in
[`../decisions.md`](decisions.md).

**Update rule.** Every commit that adds a package/adapter/workflow
updates this doc if it introduces a new test tier or pattern.

## Test tiers

| Tier | Location | Boundary |
|---|---|---|
| Pure domain and activity tests | `internal/domain`, `internal/activity`, `internal/config`, `internal/observability` | Table tests and in-memory fakes; no external services. |
| HTTP/service tests | `internal/api`, `internal/twitter`, HTTP-backed infra adapters | `httptest` and injected browser/service interfaces. |
| Workflow tests | `internal/workflow` | Temporal `testsuite.WorkflowTestSuite` with named activity mocks. |
| Adapter integration | `internal/infra` | Real Postgres, S3-compatible storage, NATS, and Temporal through testcontainers where required. |
| Scenario corpus | `test/scenarios` | YAML-driven fixture lifecycles against real workflow/activity code and test Postgres. |
| Release contract | `test/release_contract_test.go` | Parses production Compose without Docker operations and requires immutable identity propagation through every application image and the Firefox fleet image. |

For a live inventory, use `rg -n '^func Test' --glob '*_test.go' internal test`
and `rg --files test/scenarios -g '*.yaml'`.

## Tier 1 — pure Go unit tests

Every domain package + activity package + config + observability
substrate ships a `*_test.go` file with the same shape:

- No adapter imports (fixture/event/video/alias domains verified)
- In-memory fake repos for anything that would touch a database
- Table-driven where the case matrix is enum-shaped

Example — `internal/activity/ingest/activities_test.go` uses in-memory
`fakeFixtureRepo`, `fakeAliasRepo`, `fakeFetcher` — all defined in the
same test file (not `internal/testutil/`, per the "build fakes when
sharing surfaces" rule; see [architecture.md](./architecture.md#as-shipped-tree)).

FF-017 browser-lifecycle tests close an injected critical-child channel and
require `StateFailed`, `/health` 503, one `twitter.browser_failed` audit, and a
fatal result at the `cmd/twitter` process boundary. A fleet fake captures the
Docker host config and requires `on-failure` for every dynamic event browser.
An EventWorkflow test limits discovery to one outer attempt, fails its first
three `SearchTweets` activity tries, and requires the fourth to surface a
candidate after the restart window. Its default-version companion requires the
historical three-try policy for pre-FF-017 replay.

FF-026 bootstrap tests reserve a real ephemeral TCP address and require an
occupied metrics socket to reject startup before `Work` runs. A companion test
executes the public `Run` boundary in a subprocess and requires exit status 1.
A third binds an OS-assigned port, runs `Work`, and proves the shared lifecycle
drains the listener cleanly.

FF-027 monitor tests preserve the second row of a same-player brace when the
first goal is VAR-removed, require a later goal to allocate above the removed
tombstone, and reverse the provider array without swapping stored clocks. A
score-incomplete case proves a nearby new goal is inserted while the omitted
stored goal remains held, while a coherent one-minute correction must update
the original key. The Postgres integration test requires the identity-history
query to return both active and removed rows.

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

EventWorkflow cancellation tests use `RegisterDelayedCallback` with the
workflow clock and `CancelWorkflow` while the producer is in attempt spacing,
an activity, an awaited child, or vision. They require a canceled workflow
error and assert that neither a later search nor `MarkDownstreamComplete`
occurs; the vision case also rejects post-cancel pipeline activities. This
guards FF-015's producer/consumer yield points without wall-clock sleeps.

FF-007 recovery tests cover both start policy and replacement execution.
Spawner unit tests require typed `ALLOW_DUPLICATE_FAILED_ONLY`, no workflow
execution/run timeout, duplicate-start idempotency, and propagation of real
start errors. Workflow tests restore nine completed attempts, terminal and
pending candidate ownership, and a live asset; they require only attempt ten
to search, only the pending and new candidates to run, and the prior asset to
remain in the final dedup/output pool. A default-version test proves histories
created before FF-007 do not gain recovery activities or progress writes on
replay.

FF-002 workflow tests cover both sides of the child boundary. VideoWorkflow
tests exhaust all configured download and hash retries and require typed failed
outputs; the hash result must retain its staging key, while cancellation must
remain an error. EventWorkflow tests require `failed` candidate persistence,
hash-staging deletion, and captured-URL fallback for an unexpected child
failure. Explicit `OnGetVersion(...).Return(DefaultVersion)` cases preserve the
old child-error command sequence for replay of histories created before the
fix.

`PersistActivities` promotion tests inject failures into the durable tail.
They prove that a rank failure after share insertion is repaired on retry, an
uncertain staging-delete response does not require a second source copy, and
ordinary retries retain exactly one asset and share while still completing
rank repair, cleanup, and the workflow dirty-signal contract. A pre-existing
deterministic asset with mismatched immutable storage identity fails closed.

## Tier 2 — adapter integration (testcontainers)

Integration coverage currently includes `pg`, `s3`, `nats`, and `temporal`.
Each covered adapter spins its real container in the test process.

The pg recovery test verifies that `attempts_completed` advances monotonically
inside downstream metadata and that terminal versus pending candidate state is
loaded in stable discovery order.

Enablement mechanics:
- `--network=host` on the `test` make target — testcontainers-go
  connects to the containers by their exposed ports on host
- `/var/run/docker.sock` mounted into the test container so the
  test harness can start sibling containers
- Skip with `-short` (all testcontainers tests check `testing.Short()`
  at the top and skip)

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

## Tier 1.7 — Twitter service unit tests (`internal/twitter`)

The Twitter service tests run without Playwright, Firefox, or a browser.
Pattern: a `sessionBrowser` interface (`browser_iface.go`) abstracts
the browser operations Service depends on (VerifySession,
ReplaceCookies, GetCookies, Navigate). Tests inject `fakeBrowser`
from `auth_test.go` that lets each test set per-method behaviour +
inspect call counts under a mutex.

The files divide by responsibility:

- `cookies_backup_test.go` — fingerprint stability,
  value-rotation detection, `auth_token` guard on write and read,
  domain filtering, concurrent writer/reader safety, and mtime advancement.
- `auth_test.go` — first-boot no-cookies, happy path,
  warm-path skip, TTL expiry re-verifies, external reload
  (VNC-container-simulated), verify failure escalates to
  `StateUnauthenticated`, browser failure escalates to `StateFailed`,
  **five concurrent EnsureAuthenticated callers dedupe to 1 verify
  via warm-path**, `BackupCookies` fingerprint dedupe (unchanged
  cookies skip write), `BackupCookies` rewrite-on-rotation, all
  `/authenticate` + `/auth/verify` HTTP paths (POST-only guard,
  reauth config env-var passthrough, fallback message).
- `browser_cookies_test.go` — Playwright→domain cookie
  conversion: nil `SameSite` doesn't panic, `SameSite` preserved,
  round-trip stability.
- `search_test.go` — search helpers: tweet-ID + username
  URL extraction, truncated-snowflake detection, exclude-ID
  normalization, result age computation, `/search` HTTP guards
  (method-not-allowed, empty query, malformed JSON), search-URL
  builder, truncate.

The mtime-detection cases deliberately cross one-second filesystem timestamp
granularity and dominate this package's unit-test runtime.

`TestStatusExposesBuildIdentity` also guards the Twitter `/status.build`
payload consumed by the production release verifier.

## Release contract test

`test/release_contract_test.go` parses `docker-compose.prod.yml` as data. It
requires worker, API, Twitter, and Twitter VNC to receive `GIT_SHA`, `BUILT_AT`,
and `IMAGE_TAG`; it also requires the worker's `FIREFOXFLEET_IMAGE` to carry the
same immutable tag. This test is in `make test-short` and performs no Docker or
production operation. Shell syntax is checked separately with
`bash -n scripts/deploy-prod.sh`, and Compose interpolation is validated with
synthetic identity values plus `docker compose ... config --quiet`.

## Tier 3 — synthetic end-to-end scenarios

YAML scenario harness lives at [`test/`](../test/) with
scenarios under `test/scenarios/<suite>/<name>.yaml`. Design in
[`proposals/test-corpus.md`](design/proposals/test-corpus.md).

**How it works** (recap):
- One testcontainer Postgres per test binary run (shared across
  scenarios via TRUNCATE between)
- One httptest.Server mocking api-sports.io (reconfigured per scenario
  via SetResponses)
- One `testsuite.WorkflowTestSuite` per scenario (in-memory Temporal)
- Real workflow + activity code executed against the real pg
- Activity clock injected from scenario's `manual_date` for
  determinism

**Enforcement — git test gates** (installed via `make hooks`, which sets
`core.hooksPath` → [`.githooks/`](../.githooks/)): `pre-commit` runs
`make test-short` (fast — compile + unit, no containers); `pre-push` runs
the full `make test` (integration/testcontainers + this scenario suite).
The pre-push gate is what would have caught the enum-refactor
scenario rot (red since `d123404`) the day it landed rather than weeks
later. Host-agnostic (local git, not tied to any forge); each clone
activates once with `make hooks`. Bypass only with `--no-verify`.

**Suites** (each a subdirectory under `test/scenarios/`):
- `basic/` — happy paths, sanity checks
- `debounce/` — symmetric counter behavior (increment, decrement, cap,
  floor, threshold flip)
- `faults/` — API 500, timeout, rate-limit
- `edge_cases/` — postponed, hat-trick, halftime, FT-completion,
  scorer refinement, red card, and score/event reconciliation

The corpus itself is the scenario inventory. Its suites cover ingest, symmetric
debounce, vendor failures, postponed/half-time behavior, multi-event identity,
unknown-player refinement, terminal completion, and tracked card types. Do not
copy its filenames or counts into this ledger; add the YAML and its assertions
in the same change as the behavior.

FF-014 adds two complementary score/event cases: a retained goal omitted from
the provider array while the score still requires it, and a played terminal
score containing a goal that was never observed. The former completes from the
retained stored inventory; the latter remains active because the completion
parity gate fails.

**Run:** `make test-corpus` — runs just the harness. `make test`
includes it. `make test-short` excludes it (no docker).

The dev-only programs under [`scripts/`](../scripts/) cover real adapter and
workflow paths that the deterministic suite cannot. Read the program before
running it; several contact API-Football, Temporal, Garage, Twitter, or the
Docker daemon.

## Running tests

The main make targets are Docker-mounted; no host Go installation is required:

```
make test-short   # pure-Go + httptest tiers; no testcontainers
make test         # full suite including testcontainers and scenarios
make test <pkg>   # selected package
```

The `test-short` target is the pre-commit gate (fast, catches most
regressions). The `test` target is the pre-push gate (slow, exercises
real integrations).

## Coverage floors

Plan §12 called for per-package coverage floors as part of the CI
gate. The current gates require compilation and passing tests but do not
enforce coverage floors. This remains feature-scope candidate
[`AUD-DESIGN-COVERAGE`](./todo.md#audit-intake-requiring-current-code-validation),
not an implicit implementation commitment.

## Cross-refs

- Plan §12 — [rebuild-plan.md §12](design/rebuild-plan.md#12-testing)
- Adapter test template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
- Workflow test pattern — [orchestration.md § Testing shape](./orchestration.md#testing-shape)
- Live smoke scripts — [`scripts/`](../scripts/)
