# testing.md — Go rebuild ledger

**Purpose.** As-shipped testing surface — where the 494 tests live,
which tiers they cover, how each package's test file is structured,
how to run them.

Cross-refs [`../rebuild-plan.md`](design/rebuild-plan.md) §12 for the
full testing intent. Divergences from §12 live in
[`../decisions.md`](decisions.md).

**Update rule.** Every commit that adds a package/adapter/workflow
updates this doc if it introduces a new test tier or pattern.

## Test count by tier (2026-08-15)

494 tests across the internal/ tree + 19 scenarios in test/scenarios/:

| Tier (directory) | Location | Count | Runtime |
|---|---|---|---|
| Domain (pure Go) | `internal/domain` — fixture, event, video, alias, discovery, vision | 142 | <100ms total |
| Infra adapters | `internal/infra` — httptest units (apifootball, twitter, syndication, wikidata, llm, event, ffmpeg, firefoxfleet, wikipedia) + testcontainers integration (pg, s3, nats, temporal) | 182 | httptest <200ms; testcontainers ~30-60s |
| Activity (pure Go) | `internal/activity` — ingest, livefeed, monitor, video, vision | 62 | <100ms total |
| Twitter service unit (fake sessionBrowser) | `internal/twitter` | 46 | ~5s (mtime tests sleep 1.1s to detect fs granularity) |
| Workflow (testsuite.WorkflowTestSuite) | `internal/workflow` | 31 | <100ms per workflow |
| Observability | `internal/observability` — logging, metrics, vocabulary | 16 | <10ms |
| API | `internal/api` — dto, handlers, router | 10 | <50ms |
| Config | `internal/config` | 2 | <10ms |
| Bootstrap | `internal/bootstrap` | 2 | <10ms |
| Synthetic e2e | `test/scenarios` — basic 5, debounce 5, faults 3, edge_cases 6 | 19 scenarios | ~2.4s full corpus |

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

## Tier 1.7 — Twitter service unit tests (`internal/twitter`)

The Twitter service (`internal/twitter/`) is exercised by 46 unit
tests that run without Playwright / Firefox / a browser at all.
Pattern: a `sessionBrowser` interface (`browser_iface.go`) abstracts
the browser operations Service depends on (VerifySession,
ReplaceCookies, GetCookies, Navigate). Tests inject `fakeBrowser`
from `auth_test.go` that lets each test set per-method behaviour +
inspect call counts under a mutex.

Four files:

- `cookies_backup_test.go` (11 tests) — fingerprint stability,
  value-rotation detection, `auth_token` guard on write and read,
  domain filter, **atomic-write-no-torn-reads under 5×20×50
  concurrent writer/reader stress**, mtime advancement.
- `auth_test.go` (22 tests) — first-boot no-cookies, happy path,
  warm-path skip, TTL expiry re-verifies, external reload
  (VNC-container-simulated), verify failure escalates to
  `StateUnauthenticated`, browser failure escalates to `StateFailed`,
  **five concurrent EnsureAuthenticated callers dedupe to 1 verify
  via warm-path**, `BackupCookies` fingerprint dedupe (unchanged
  cookies skip write), `BackupCookies` rewrite-on-rotation, all
  `/authenticate` + `/auth/verify` HTTP paths (POST-only guard,
  reauth config env-var passthrough, fallback message).
- `browser_cookies_test.go` (3 tests) — Playwright→domain cookie
  conversion: nil `SameSite` doesn't panic, `SameSite` preserved,
  round-trip stability.
- `search_test.go` (10 tests) — search helpers: tweet-ID + username
  URL extraction, truncated-snowflake detection, exclude-ID
  normalization, result age computation, `/search` HTTP guards
  (method-not-allowed, empty query, malformed JSON), search-URL
  builder, truncate.

Slowest test: `~1.1s` (mtime-detection tests sleep to defeat
1-second-granularity filesystems). Total package runtime ~5s.

## Tier 3 — synthetic e2e — SHIPPED (Phase 1a)

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
the full `make test` (integration/testcontainers + this scenario suite,
~2 min). The pre-push gate is what would have caught the enum-refactor
scenario rot (red since `d123404`) the day it landed rather than weeks
later. Host-agnostic (local git, not tied to any forge); each clone
activates once with `make hooks`. Bypass only with `--no-verify`.

**Suites** (each a subdirectory under `test/scenarios/`):
- `basic/` — happy paths, sanity checks
- `debounce/` — symmetric counter behavior (increment, decrement, cap,
  floor, threshold flip)
- `faults/` — API 500, timeout, rate-limit
- `edge_cases/` — postponed, hat-trick, halftime, FT-completion,
  scorer refinement, red card

**Currently shipped scenarios (19, all passing in ~2.4s):**

basic/ (5):
- `ingest_happy_path` — daily ingest, 2 staging fixtures
- `ingest_manual_ids` — ManualFixtureIDs re-ingest path
- `ingest_terminal_at_seed` — API returns FT → completed at ingest
- `ingest_activate_at_seed` — kickoff within 30-min window → activate
- `ingest_existing_preserves_state` — merge preserves activated_at

debounce/ (5):
- `var_overturn` — 6 cycles: 3 present trigger → 3 absent hit-zero → soft-delete
- `flicker_no_reset` — 7 cycles oscillating; symmetric counter (NOT Python's reset). Ends stable at 3.
- `threshold_flip` — 8 consecutive present; downstream_triggered flips once, counter caps
- `multiple_goals` — 2 different players score, independent debounce per event
- `removed_terminal` — soft-removed events stay terminal; API bringing back same natural_key doesn't re-track

faults/ (3):
- `api_500_recovers` — transient 500 → retry recovers, no state impact
- `api_persistent_500` — sustained 500 outage doesn't burn debounce budget
- `api_429_rate_limited` — 429 rate limit same protection as 500 outage

edge_cases/ (6):
- `postponed_mid_play` — PST mid-play doesn't false-delete goals (2026-07-09 pause fix)
- `hat_trick` — same player scores 3 times; seq assignment (1, 2, 3) works
- `halftime_pause` — 1H goal survives HT pause; every match hits this
- `fixture_completes_at_ft` — goal mid-debounce when the match ends still reaches count=3 and triggers; FT is Terminal (not InPlay), so absence votes don't false-drop it
- `player_refinement` — API reports a goal with null scorer then refines it; the "unknown" and known-scorer natural_keys differ, so a refinement looks like a new event
- `red_card_detected` — red cards are first-class events with independent debounce; yellows ignored, second-yellow tracks like a red

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

- Plan §12 — [rebuild-plan.md §12](design/rebuild-plan.md#12-testing)
- Adapter test template — [architecture.md § Adapters](./architecture.md#adapters--as-shipped-template)
- Workflow test pattern — [orchestration.md § Testing shape](./orchestration.md#testing-shape)
- Live smoke scripts — [`scripts/`](../scripts/)
