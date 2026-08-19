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
| Configuration contract | `internal/config/contract_test.go` | Derives each binary's env ownership from Go tags and checks `.env.example`, both Compose files, environment routing, and cookie-directory mounts. |
| Release contract | `test/release_contract_test.go` | Parses production Compose without Docker operations and requires immutable identity propagation plus the stack-wide ffmpeg CPU budget. |
| Tooling contract | `test/tooling_contract_test.go` | Requires exact Go, golangci-lint, and Air versions across build files plus the intended commit/push hook targets. |

For a live inventory, use `rg -n '^func Test' --glob '*_test.go' internal test`
and `rg --files test/scenarios -g '*.yaml'`.

## Tier 1 — pure Go unit tests

Every domain package + activity package + config + observability substrate
keeps tests in the package they verify. Small packages use one `*_test.go`;
larger packages split tests by responsibility while sharing package-private
fakes and setup helpers:

- No adapter imports (fixture/event/video/alias domains verified)
- In-memory fake repos for anything that would touch a database
- Table-driven where the case matrix is enum-shaped

Example — `internal/activity/ingest/activities_test.go` owns the in-memory
`fakeFixtureRepo`, `fakeAliasRepo`, and `fakeFetcher`; focused fixture,
retention, metadata, and tracked-team cases live in sibling test files in the
same package. There is no speculative `internal/testutil` package. This is the
standard Go colocated-test layout, not production/test code mixing.

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

FF-055 domain tables cover score-derived home, away, tied, incomplete,
shootout, and exceptional winner states. The monitor regression starts with a
stored 1–0 leader and requires a later 1–1 response to clear both winner fields
and emit a structural update. Provider-vote and Postgres completion truth
tables independently reject `PEN` without a present, non-tied shootout score.

FF-028 API tests require the default five-minute presign to produce a
four-minute redirect cache, longer presigns to retain the five-minute cap, and
short or unset lifetimes to disable redirect caching. The handler-level test
also verifies the derived header on a real 302 response.

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

The EventWorkflow suite is split into discovery durability/retry, candidate
failure, publication, pre-hash ownership, and perceptual-dedup files. Shared
Temporal environment builders stay in `event_test.go`; all files remain in
`workflow_test` so the split changes organization, not the tested boundary.

FF-007 recovery tests cover both start policy and replacement execution.
Spawner unit tests require typed `ALLOW_DUPLICATE_FAILED_ONLY`, no workflow
execution/run timeout, duplicate-start idempotency, and propagation of real
start errors. Workflow tests restore nine completed attempts, terminal and
pending candidate ownership, and a live asset; they require only attempt ten
to search, only the pending and new candidates to run, and the prior asset to
remain in the final dedup/output pool. A default-version test proves histories
created before FF-007 do not gain recovery activities or progress writes on
replay.

FF-025 spawner tests require two unchanged snapshots before an exact-run
termination and failed-only replacement. Separate cases prove that history or
state-transition movement resets the quiet clock, a recent activity heartbeat
also resets it, and describe or termination errors never start a replacement.
The derived bound tests cover its 30-minute floor plus long attempt-spacing and
query-timeout configurations.

FF-002 workflow tests cover both sides of the child boundary. VideoWorkflow
tests exhaust all configured download and hash retries and require typed failed
outputs; the hash result must retain its staging key, while cancellation must
remain an error. EventWorkflow tests require `failed` candidate persistence,
hash-staging deletion, and captured-URL fallback for an unexpected child
failure. Explicit `OnGetVersion(...).Return(DefaultVersion)` cases preserve the
old child-error command sequence for replay of histories created before the
fix.

FF-022 workflow tests cover the replacement boundary. Two separately staged
downloads with one MD5 must run `HashVideo` exactly once and preserve both
popularity votes. If the first claimant exhausts three hash attempts, the next
claimant must hash its own staging object and reach vision without being marked
duplicate. Cancellation while that hash is pending must schedule no forensic,
cleanup, promotion, or finalization commands. A default-version case requires
pre-FF-022 histories to keep calling the registered VideoWorkflow child and
never add a direct download activity.

FF-041/FF-005 tests require ffmpeg's dense filter to apply grayscale and a
640-pixel area reduction before PNG serialization, require `HashVideo` to emit
the interval-specific version, and reject sequences shorter than the matching
window without an error retry. Workflow tests prove that deterministic rejects
skip vision, incompatible stored versions never compare, and pre-FF-041 blank
Temporal fields normalize to the legacy identity. The operational migration
contract hashes `schema.sql` and requires its VerifySchema stamp to remain
exact; the Postgres integration test removes the fresh-schema column, seeds an
old asset, applies the real migration, and verifies both legacy backfill and
the new stamp.

FF-034 workflow tests require candidate processing to launch even when every
observation-insert retry fails, while the failed attempt remains uncheckpointed
and the downstream checklist stays open. A separate case exhausts the terminal
UPSERT and proves EventWorkflow cannot complete. The default-version case
requires pre-FF-034 histories to retain `StoreCandidate` before launch and the
legacy best-effort `RecordCandidateOutcome` command sequence.

FF-050 unit coverage pins the typed Temporal workflow log envelope and
non-negative deterministic duration arithmetic. The existing EventWorkflow
suite exercises the instrumented search and candidate branches, so any added
workflow command or reordered behavior remains visible through its activity
and version assertions. `test/matchday_status_contract_test.go` syntax-checks
the operator script, requires environment and Firefox scope derivation, and
rejects mutation statements or a status query without an explicit read-only
transaction.

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
inside downstream metadata and that terminal versus observed candidate state,
including complete evidence, loads in stable discovery order. A second FF-034
case calls the terminal UPSERT without a prior observation row, retries it, and
requires one complete terminal row.

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
  immutable reauth-config injection, fallback message).
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

## Release and tooling contract tests

`internal/config/contract_test.go` derives the complete worker, API, and
Twitter variable sets from the same struct tags used at startup. It requires
every non-defaulted Go variable in `.env.example`, rejects stale template keys,
checks required Compose interpolation, and verifies that application services
route only owned explicit overrides. It also preserves the environment-scoped
Firefox network, worker-only `EVENT_ENV`, and parent-directory cookie mount.
The API profile test also supplies a malformed NATS setting and proves the API
ignores it while the worker rejects it. These tests parse repository files and
process-local environment only and are part of `make test-short`.

`test/release_contract_test.go` parses `docker-compose.prod.yml` as data. It
requires worker, API, Twitter, and Twitter VNC to receive `GIT_SHA`, `BUILT_AT`,
and `IMAGE_TAG`; it also requires the worker's `FIREFOXFLEET_IMAGE` to carry the
same immutable tag. It also multiplies fixed worker replicas, per-worker
ffmpeg slots, and per-process threads, requiring the result to equal the
declared 32-thread stack budget. The test requires explicit worker environment
overrides, so `.env` defaults cannot silently multiply across replicas. This
test is in `make test-short` and performs no Docker or production operation.
Shell syntax is checked separately with
`bash -n scripts/deploy-prod.sh`, and Compose interpolation is validated with
synthetic identity values plus `docker compose ... config --quiet`.

`test/tooling_contract_test.go` derives the declared Go and golangci-lint
versions from the Makefile, requires exact semantic versions, and checks every
Go build stage against that declaration. It also prevents `air@latest` and
golangci-lint `latest` from returning and requires the fast/full hook targets.
The contract is part of `make test-short` and does not invoke Docker.

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

**Enforcement — git engineering gates** (installed via `make hooks`, which sets
`core.hooksPath` → [`.githooks/`](../.githooks/)): `pre-commit` runs
`make check-short` (format, tidy, vet, lint, compile, and unit tests);
`pre-push` runs the full `make check`, adding integration/testcontainers and
this scenario suite. The pre-push gate is what would have caught the enum-refactor
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

FF-029 is covered below the scenario layer because it is an HTTP-adapter and
activity-retry contract: adapter tests distinguish syndication metadata 403
from CDN byte-download 403, the activity test preserves the terminal/transient
split, and the workflow test requires all four transient download attempts.

FF-012 similarly spans three unit layers: the LLM adapter types malformed 2xx
JSON, `ValidateClip` marks all permanent model/config/response sentinels
non-retryable, and WorkflowTestSuite proves permanent vision failure runs once
while transient failure runs the configured three attempts.

FF-030 has a pure domain regression for the rank comparator's final share-ID
tiebreaker. It asserts both directions and preserves equality for one identical
share, so an unordered repository read cannot change a complete tie.

FF-031's domain regression supplies an unknown API minute with both visible and
absent broadcast clocks. Both valid soccer cases remain unverified with no
matched minute, while non-soccer and screen-recording gates still reject.

FF-057 covers both live scorebug families: the Abdelkarim API-30′ event with a
`28:56 1H` sampled clock, and the Zizo API-51′ event with a reset `05:25 2H`
clock. Domain tests require the latter to normalize to absolute minute 50,
retain continuous `50:25 2H`, reject an explicit `05:25 1H` conflict, and
soft-keep rather than verify the same low clock when period evidence is absent.
Parser tests retain compact-stoppage period provenance. Activity tests pin the
nullable period schema and normalized readings; WorkflowTestSuite requires all
three raw observations and readings in a clock reject's `outcome_detail`.
Exact-boundary cases require `45:25 2H` to verify against either API-46′
continuous time or API-90′ reset time, and `15:10 ET2` against either ET2
convention, while retaining the unchanged ±1 tolerance. The candidate-replay
Postgres integration test proves exact selection, transactional checklist
registration, prior-verdict preservation, terminal-detail merging, retry
idempotency, workflow-identity drift rejection, JSON-null outcome handling,
exact malformed-envelope normalization, and post-run verification.

FF-032 makes the LLM mock's captured-request buffer concurrency-safe. The
adapter's concurrency-cap test now exercises parallel handlers without racing
inside the harness, so `make test-race` remains a trustworthy release gate.

**Run:** `make test-corpus` — runs just the harness. `make test`
includes it. `make test-short` excludes it (no docker).

The dev-only programs under [`scripts/`](../scripts/) cover real adapter and
workflow paths that the deterministic suite cannot. Read the program before
running it; several contact API-Football, Temporal, Garage, Twitter, or the
Docker daemon.

## Running tests

The main make targets are Docker-mounted; no host Go installation is required:

```
make check-short  # every fast commit gate; no testcontainers
make check        # every gate, including integrations and scenarios
make test-short   # pure-Go + httptest tiers; no testcontainers
make test         # full suite including testcontainers and scenarios
make test <pkg>   # selected package
```

`test-short` and `test` remain focused test runners. `check-short` and `check`
are the authoritative engineering gates used by Git hooks.

## Coverage floors

Plan §12 called for per-package coverage floors. The
[2026-08-17 independent audit](./design/audits/audit-2026-08-17-codex.md#prior-audit-disposition)
rejected a global percentage as an implementation target: integration-only
adapters make package percentages misleading, and a number does not prove the
changed invariant. Each issue adds boundary-specific regression coverage while
the gates continue to require all tests to pass.

## Cross-refs

- Plan §12 — [rebuild-plan.md §12](design/rebuild-plan.md#12-testing)
- Adapter test template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
- Workflow test pattern — [orchestration testing](./orchestration/testing.md#testing-shape)
- Live smoke scripts — [`scripts/`](../scripts/)
