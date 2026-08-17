# architecture.md — Go rebuild ledger

**Purpose.** This doc records **what has actually shipped** in the
Go rebuild — the concrete tree, which packages have real code vs
which are stubs, which adapters are live, which domain packages have
what. It's the ledger against which [`../rebuild-plan.md`](design/rebuild-plan.md)
is the intent.

If code and plan diverge, the divergence is logged in
[`../decisions.md`](decisions.md) with a date and reason. If code
and plan match, no entry — silence == alignment.

**Update rule.** Every commit that adds/removes a package, changes
an adapter shape, or lands a new domain type updates this doc in
the SAME commit. Not the next commit. Same commit.

## As-shipped tree

```
found-footy/
├── cmd/                                 deployable binaries; each imports from internal/
│   ├── api/main.go                      ✓ Chi read surface (SSE is vedanta-systems')
│   ├── twitter/main.go                  ✓ T/a+T/b+T/c: real Playwright-Go service (ephemeral profile + idle-CPU prefs)
│   └── worker/main.go                   Temporal worker; registers Ingest + ActivePoll + StagingPoll + Event + Video workflows
├── internal/
│   ├── domain/                          active domain logic + explicit extension stubs
│   │   ├── fixture/                     ✓ D1: model + State + Repo + tests
│   │   ├── event/                       ✓ D2: model + State + Repo + tests
│   │   ├── video/                       ✓ D3 + V/2 + V/3a: model + Repo + rank + perceptual dHash + Match + hard-filter + tests
│   │   ├── alias/                       ✓ canonical-team record + shared text operations; resolver removed 2026-08-16
│   │   ├── team/                        ✓ TrackedTeam set — tracked-teams-cache ingest filter (team.go + repo.go)
│   │   ├── discovery/                   ✓ Query builder (2026-07-22) + real EventWorkflow (O3/d, 2026-07-23)
│   │   │   ├── doc.go                   Package doc — query construction, URL extraction, source scoring
│   │   │   ├── query_builder.go         BuildTwitterQuery, ErrEmptyQuery, ErrEmptyPlayerName (D1/D4b/D4c/D4d/D7 per twitter-search-query.md)
│   │   │   └── query_builder_test.go    name, particle, dedup, fallback, and safeguard cases
│   │   ├── vision/                      ✓ D5 (2026-07-28): clock.go + evaluate.go + schema.go + tests — clip-clock validation, wired into EventWorkflow consumer
│   │   ├── session/                     ⊘ unused extension stub; lifecycle lives in infra/firefoxfleet
│   │   └── textanalysis/                ⊘ doc.go stub — extensibility hook per plan §4
│   ├── infra/                           live infrastructure adapters
│   │   ├── pg/                          ✓ S2: pool + instruments + schema.sql + VerifySchema drift guard (audit P0-3) + FixtureRepo + EventRepo + AliasRepo + TeamRepo + AssetRepo/ShareRepo (#164a)
│   │   ├── nats/                        ✓ S3: client + instruments
│   │   ├── s3/                          ✓ S4: Garage client + instruments
│   │   ├── llm/                         ✓ S6: OpenAI-compatible client + typed errors + Chat
│   │   ├── temporal/                    ✓ S5: Client (with workerShutdownTimeout) + Worker
│   │   ├── apifootball/                 ✓ S7 + O1a: /status probe + /fixtures + /fixtures/{ids}
│   │   ├── twitter/                     ✓ HTTP client for the Go Twitter service + mock-backed tests
│   │   ├── syndication/                 ✓ S7 + T/f: FetchJSON + ResolveVideo/Download (cookieless mp4) + typed taxonomy + tests
│   │   ├── event/                       ✓ composer (pg event_log audit ONLY — N2 removed its NATS half; Kind = 6 event_log types) · N1+N5 NatsPublisher — 3-subject live-feed (fixture.clock/update, event.video) + Envelope + source config + golden tests
│   │   ├── ffmpeg/                      ✓ V/1: probe + single/dense frame extract (single-pass fps) + faststart + semaphore + typed taxonomy + tests
│   │   └── firefoxfleet/                ✓ #160 + FF-001: per-event Firefox provisioner via Docker API — Compose-network-scoped daemon names/ownership labels/count/list/reap/release; stable event-only network alias keeps workflow addressing registry-free; idempotent lifecycle + two-fleet/one-daemon tests
│   ├── workflow/                        shipped Temporal workflows
│   │   ├── ingest.go                    ✓ O1c: IngestWorkflow
│   │   ├── active_poll.go               ✓ O2: ActivePollWorkflow (30s IntervalSpec)
│   │   ├── staging_poll.go              ✓ O2: StagingPollWorkflow (*/15 cron)
│   │   ├── event.go                     ✓ #164c: EventWorkflow — per-goal orchestrator (producer: discovery search + spawn Video children; ex-DiscoveryWorkflow)
│   │   ├── event_pipeline.go            ✓ #164c-b + #171: Selector consumer — md5 gate → vision → category-scoped perceptual dedup + IsUpgrade winner-select → promote/supersede → rank; assets/pending/inFlight state; searchDone&&inFlight==0 completion
│   │   └── video.go                     ✓ #165: VideoWorkflow child (download → hash)
│   ├── activity/                        activity packages + shared heartbeat helper
│   │   ├── ingest/                      ✓ config, roster, fixture fetch/upsert, canonical-team placeholder, and retention activities
│   │   │   ├── activities.go
│   │   │   └── activities_test.go
│   │   ├── monitor/                     ✓ config, activation, staging/live fetch, stable event-identity reconcile (FF-027), and signal/spawn support
│   │   ├── discovery/                   ✓ config/aliases/search/candidates, durable recovery checkpoints, and downstream completion
│   │   ├── video/                       ✓ DownloadAndStage, HashVideo, live-asset recovery, persistence, teardown, and ranking activities
│   │   ├── vision/                      ✓ staged-clip frame extraction + model-backed validation
│   │   ├── fleet/                        ✓ #160: ProvisionFirefox / ReleaseFirefox / ReapOrphanedFirefox / InstanceAddr — thin Temporal-activity wrapper over infra/firefoxfleet; nil-Fleet no-op when fleet disabled (FIREFOXFLEET_ENABLED=false)
│   │   ├── livefeed/                     ✓ publish-activity boundary for all NATS live-feed emits
│   │   └── heartbeat/                    shared time-based activity heartbeat loop
│   ├── api/                             ✓ Chi read API, DTOs, search, and share redirect; exact contract in docs/api.md; SSE is vedanta-systems'
│   ├── bootstrap/                       ✓ S1 + FF-026 (NOT IN PLAN — see decisions.md 2026-07-07)
│   │   └── bootstrap.go                 Deps + LIFO closer registry; fail-fast metrics/health listener; shared binary lifecycle
│   ├── config/                          ✓ S1: envconfig-based Config with per-adapter sub-structs
│   ├── observability/
│   │   ├── vocabulary/                  ✓ S1: typed Module + Action enums
│   │   ├── logging/                     ✓ S1: slog Emit() + TestEmitter for unit tests
│   │   ├── metrics/                     ✓ S1: Prometheus registry helper
│   │   └── tracing/                     ⊘ Noop tracer stub; real OTLP is deferred
│   ├── testutil/                        ⊘ empty (build as testing needs surface)
│   ├── twitter/                         Twitter *service* (browser + auth + scrape); imported by cmd/twitter
│   │   ├── browser.go                   ✓ T/a + FF-017: Firefox persistent context, cookie/session operations, and critical-child exit signal
│   │   ├── browser_iface.go             ✓ T/b: sessionBrowser interface — auth flow testable without Playwright
│   │   ├── stealth.go                   ✓ T/a: navigator.webdriver / plugins / permissions patches
│   │   ├── service.go                   ✓ T/a + T/b + FF-017: state machine, browser-loss watcher/audit, /health, /status
│   │   ├── auth.go                      ✓ T/b: EnsureAuthenticated (mtime → warm-path → verify) + BackupCookies + /authenticate + /auth/verify
│   │   ├── cookies_backup.go            ✓ T/b: Fingerprint, WriteBackup (atomic), ReadBackup, BackupFileMtime, auth_token guard
│   │   ├── search.go                    ✓ T/c: POST /search + full DOM scrape + 4-condition scroll loop + BackupCookies hook + combined verify+search + stealth jitter
│   │   └── *_test.go                    cookie, auth, browser-conversion, and search tests
│   └── usecases/                        ⊘ doc.go stub (build when cross-domain ops surface)
├── docker/twitter/                      ✓ T/b: twitter service image + entrypoint (peer of internal/)
│   ├── Dockerfile                       Playwright base + playwright-go driver + optional WITH_VNC layer (~150 MB xvfb+fluxbox+x11vnc+novnc+websockify)
│   └── entrypoint.sh                    Conditionally boots VNC daemon stack when TWITTER_VNC_MODE=true, otherwise passthrough
├── migrations/                          ⊘ EMPTY BY DESIGN (audit P0-3) — flat schema.sql + VerifySchema drift guard, no migration files; first post-cutover in-place change adds one file, squashed back into schema.sql once applied
│                                          (see decisions.md 2026-07-07)
├── scripts/                             dev-only smoke, trigger, verification, and focused probe programs
│   ├── smoke_repos/main.go              ✓ live pg + repo smoke test (dev only)
│   ├── trigger_ingest/main.go           ✓ live IngestWorkflow trigger (O1d verification)
│   ├── smoke_fleet/main.go              ✓ #160: live per-event fleet smoke — provision→healthy→release one instance (dev only; needs docker.sock + dev network)
│   └── smoke_prod_perms.sh              ✓ non-root prod image perm smoke (fleet + video scratch write paths)
├── test/                                ✓ YAML-driven scenario harness
│   ├── harness/                         ✓ testcontainer pg + mock apifootball + assertion engine
│   ├── scenarios/                       ✓ YAML corpus organized by suite
│   │   ├── basic/                       ✓ happy paths
│   │   ├── debounce/                    ✓ counter and removal scenarios
│   │   ├── faults/                      ✓ vendor-failure scenarios
│   │   └── edge_cases/                  ✓ lifecycle edge cases
│   └── scenarios_test.go                ✓ corpus runner (iterates YAML files)
├── caddy/found-footy.caddy              non-loaded reference; live routes are owned by the proxy repo
├── docker-compose.dev.yml               ✓ dev stack; air hot-reload on all 3 Go binaries
├── docker-compose.prod.yml              ✓ runs the GO codebase — LIVE prod as of the 2026-08-15 Python→Go cutover
├── Dockerfile / Dockerfile.dev          ✓ multi-stage prod + air-based dev
├── go.mod / go.sum                      ✓ Go 1.25 (bumped from 1.23 for air compat)
├── Makefile                             ✓ build/test/test-short via docker run
└── docs/                                see docs/README.md for routing
```

Legend:
- `✓` — shipped, with tests where the boundary permits deterministic coverage
- `⊘` — explicit stub or unused extension point
- No marker — not part of the rebuild (Python-era or config)

## Domain packages — as-shipped shape

Fixture, event, video, alias, vision, team, and discovery carry active logic;
session and textanalysis are unused extension stubs. The richer packages
loosely follow the layout below (matching
[rebuild-plan.md §4](design/rebuild-plan.md#4-domain-model)), but it isn't
uniform — notably **only fixture + event have a `state.go`** (the rest aren't
state machines):

```
domain/<name>/
├── <name>.go               model type + New() constructor
├── state.go                state transitions (fixture + event only; others omit it)
├── repo.go                 Repo interface + ErrNotFound sentinel
└── <name>_test.go          unit tests — pure Go, no adapters
```

**Cross-cutting rule (audit-verified):** domain packages import nothing
from `internal/infra/*`. Repos are interfaces defined in domain;
implementations live in `internal/infra/pg/` and satisfy them
structurally.

### fixture domain (D1)

Core type `fixture.Fixture` with `State` (staging/active/completed),
API-mirror fields (`APIStatus`, `APIElapsed`, `APIExtra`, scores), and
domain-managed timestamps (`ActivatedAt`, `CompletedAt`,
`LastActivityAt`, `LastPolledAt`).

State transitions:
- `Activate(at) → active` (sets ActivatedAt, LastActivityAt)
- `Complete(at) → completed` (sets CompletedAt, LastActivityAt)
- `Reschedule(newKickoff, at) → staging` (clears ActivatedAt; for PST/moved fixtures)
- `UpdateFromPoll(status, elapsed, extra, scores, at)` — refreshes
  API-mirror fields + LastPolledAt without changing state

Predicates: `ShouldActivateNow(now, window)` — used by both the ingest
activity (at-upsert-time activation for imminent kickoffs) and the
ActivePollWorkflow's `ActivateUpcoming` step.

Repo methods shipped in `internal/infra/pg/fixture_repo.go`:
`Get`, `Upsert`, `ListByState`, `ListActiveIDs` (cheap ID-only
projection for ActivePollWorkflow's batched API call),
`ListStagingBeforeKickoff`, `FixtureReadyToComplete` (the completion-contract
evaluator, including played-result score/stored-goal parity; see the
[FF-014 decision](decisions/2026-08-16-score-backed-goal-removal.md)),
and the two-part retention pair (#176): `PruneCompleted` (hard-delete clipless
aged fixtures) + `ListReclaimableEventIDs` (events of clip-bearing aged fixtures
with live shares → the workflow's `DestroyEvent` byte-reclaim loop; keeps rows as
410 tombstones per [decisions.md 2026-08-11](decisions.md)).

### event domain (D2)

Core type `event.Event` — **no `State` enum**; the lifecycle lives in three
fields: `DebounceCount` (0–3 symmetric counter), `DownstreamTriggered` (one-way
FALSE→TRUE latch, flips the moment DebounceCount first reaches 3), and
`Removed`/`RemovedReason`/`RemovedAt` (atomic soft-delete on hitZero). Captures
the 3-poll invariant Python enforced via monitor-cycle registration counts.

Repo methods shipped in `internal/infra/pg/event_repo.go`:
`Get`, `GetByNaturalKey`, `Insert(ctx, e, workflowID)` (atomic seed —
`debounce_count=1` + first presence vote for a **known** scorer, but
`debounce_count=0` + **no** vote for an unknown-scorer placeholder, per G1),
`DeleteUnknownEvent` (hard-delete a lingering `debounce_count=0` placeholder),
`UpdateMutableFields`, `Upsert`, `ListPending`, `ListByFixture` (visible rows),
`ListAllByFixture` (FF-027 active + removed identity history),
`EventsAwaitingDiscovery` (the discovery spawn set),
`RegisterEventPresence` (increment, cap 3, flips downstream_triggered on first
hit), `RegisterEventAbsence` (decrement, floor 0, atomic soft-delete on hitZero
with reason='var'), `RegisterDownstreamWorkflow` (inserts the
`event_downstream_workflows` checklist row), `RegisterVideoValidationWorkflow`
(monotonic download-attempt counter). Debounce model per decisions.md
2026-07-07 symmetric-counter + 2026-08-05 unknown-scorer entries.

### video domain (D3)

Core types `video.Asset` and `video.Share` — the split from Python's single
`video` collection that supports the URL-stability + rank invariants
(`rebuild-plan.md` §3/§4). Post-#166 `Asset` is `event_id`-scoped and carries a
per-frame `frame_hashes` dHash sequence (md5 exact-match + `UNIQUE(event_id,
md5)`; the old whole-clip `perceptual_hash` UNIQUE is retired).

Beyond the model, the package owns the dedup + quality logic (pure, table-
tested): `hash.go` (`DHash`/`DHashPNG`), `match.go` (`Match` — the
offset-tolerant sliding window), `filter.go` (`HardFilter` pre-download gate),
`quality.go` (`IsUpgrade`/`ClipQuality` winner-selection — wired post-vision #171),
and `rank.go` (`CompareShares` — the deterministic frontend tie-break).

### alias domain (D4)

The Wikipedia→Wikidata resolver was removed on 2026-08-16 after live tests
showed its broad alias set reduced Twitter recall. The package now has two
active responsibilities:

- `TeamAlias` and `alias.Repo` retain the `team_aliases` compatibility record.
  Ingest calls `EnsureAliasPlaceholders` so each observed team has a stable
  canonical API-Football name. The old resolution columns and methods remain
  in the schema/API but have no production writer.
- `TokenizePlayerName` performs the deterministic text normalization used by
  the discovery query builder: transliterate to ASCII, split punctuation and
  dashes, drop short/digit/particle noise, and preserve distinct tokens.

`EventWorkflow` reads the stored canonical name, falls back to the event's team
name, and passes no resolved aliases to `discovery.Build`. The builder combines
all significant player-name tokens, the quoted canonical team name, and a
derived team abbreviation. The retired resolver design remains in
[`team-aliases.md`](design/proposals/team-aliases.md) and
[`alias-entity-resolution.md`](design/proposals/alias-entity-resolution.md).

### vision domain (D5) — shipped 2026-07-28

Clip-validation logic, pure + table-tested (no I/O, no model). Ports the Python
clock parsers with a period-awareness fix.

- `clock.go` — scorebug field parsers (`parseClockField`, `parseAddedField`,
  `parseStoppageClockField` — the last accepts both `01:48` and `+1:48` model
  output) + `periodOf` (the H1/H2/ET1/ET2 map, verified against
  real API-Football data).
- `evaluate.go` — `Evaluate(frames, Expected, tol)`: soccer/screen majority
  gates → period-aware clock check → `Outcome` (verified/unverified/rejected).
  Strictness: ±1 minute, strict at halftime / lenient at ET (see decisions.md).
- `schema.go` — `FrameObservation` (per-frame JSON) + `VisionResponse`
  (`{Frames}`, the `response_format` json-schema, exactly-3 positional frames) +
  `DefaultPrompt`.

Consumed by `internal/activity/vision.ValidateClip`: fetch staged clip →
`ffmpeg.ExtractFrame` @25/50/75% → one multi-image structured-output vision call
→ `Evaluate`. **Wired into EventWorkflow's consumer** (`event_pipeline.go`, fired
async per unique clip); the LLM adapter's `ResponseFormat` + `DisableThinking`
fields (rung 1) exist for this call. At the activity boundary, typed permanent
LLM failures (invalid response/request, missing model, or auth) become
non-retryable Temporal ApplicationErrors; transient model and infrastructure
classes retain the workflow's bounded retry policy (FF-012).

### team domain (D6)

Core type `team.TrackedTeam` (id, name, league, season, refreshed_at) + a
`team.Set` for O(1) membership. Backs the tracked-teams fixture filter:
`RefreshTrackedTeamsIfStale` builds the cache from league rosters, `Replace`
does an atomic truncate+COPY, and `FetchFixturesForDay` filters against it.
Per-team provenance (one row/team with league+season) enables the
promotion/relegation reasoning the Python single-doc `top_flight_cache`
couldn't. Repo (`team_repo.go`): `List`, `Replace`, `OldestRefreshedAt`.

## Adapters — as-shipped template

Every live adapter under `internal/infra/*/` follows the pattern
established by the pg adapter (S2):

```
infra/<name>/
├── client.go               constructor: New(ctx, cfg, instruments)
├── instruments.go          RegisterMetrics(reg, log) → *Instruments (bundle)
├── <name>_test.go          testcontainers-go OR httptest-based test
└── doc.go                  package-level docstring + "why this shape" notes
```

The `Instruments` bundle carries labeled counters/histograms + a
prometheus.Collector for scrape-time gauges + a framework-native
tracer where the adapter's library supports one (pg has QueryTracer,
NATS has connection callbacks, LLM has httptrace).

**Cross-cutting rule:** every adapter's `New(...)` returns
`(client, error)`, does NOT panic, and does NOT log at info level from
package init — all lifecycle logging goes through the
`bootstrap.Deps.Log` + vocabulary Action enums.

Adapter-specific notes:

- **pg**: pool via pgxpool; QueryTracer emits per-query duration histograms
  + pool-stats collector. Schema in `schema.sql` mounted into dev postgres
  via `/docker-entrypoint-initdb.d/` (fresh volume only) AND into
  testcontainers via `WithInitScripts`.
- **temporal**: Client wraps SDK client with `workerShutdownTimeout`
  accessor; NewWorker seeds `Options.WorkerStopTimeout` from Client if zero.
- **llm**: types.go owns domain-shaped `ChatRequest`/`ChatResponse`;
  classifyError translates HTTP status codes to typed errors
  (ErrRateLimited, ErrCapExceeded, etc.) and maps malformed successful wire
  responses to `ErrInvalidJSON`.
- **syndication**: metadata resolution and CDN byte download use separate 403
  classes. Metadata 403 is terminal `ErrGeoRestricted`; CDN 403 is transient
  `ErrCDNForbidden`, allowing the enclosing activity retry to resolve a fresh
  variant URL without exposing the signed URL in errors (FF-029).
- **apifootball**: getJSON helper handles auth (`x-apisports-key` per
  doc) + rate-limit-header parsing (per-minute + daily distinct) +
  error classification. `/fixtures` (single + by-IDs) landed in O1a.
  `ListFixturesByIDs` accepts any-size input, chunks internally at
  `IDsBatchLimit=20` (exported const, sourced from vendor doc), fires
  per-chunk HTTP calls in parallel via `errgroup`, returns
  `(fixtures, failedIDs, err)`. Partial failure surfaces as non-empty
  `failedIDs`. See decisions.md 2026-07-09 refactor entry.

**Twitter service note.** `internal/infra/twitter/` is the HTTP client;
tests pass against a mock. Dev and prod Twitter containers run the Go browser
service. The archived Python service is rollback evidence only.

`twitter.Client.Search(ctx, addr, req)` takes a **per-call base address**
(#160): empty `addr` → the shared `TwitterConfig.BaseURL` (pre-#160
behavior); a non-empty `addr` → that event's dedicated fleet instance,
derived by `firefoxfleet.InstanceAddr(eventID)`. The EventWorkflow decides
which by `FleetEnabled` and threads it through `SearchTweetsInput.InstanceAddr`.
This is how one HTTP client fans searches across N per-event Firefox
containers without a router or registry — the address is a pure function
of the event ID. Client construction validates only static configuration and
performs no readiness probe. A browser service that is starting or temporarily
unreachable fails the current activity attempt; a later Temporal retry uses the
same client and observes the recovered service (FF-016).

## Package dependency direction (audit-verified)

```
cmd/*
  ↓
internal/workflow/          (workflow definitions)
  ↓
internal/activity/*/        (activities — the boundary)
  ↓                              ↓
internal/domain/*/          internal/infra/*/  (adapters)
                                   ↑
                                   └── config, observability, bootstrap
```

**Never happens:**
- `internal/domain/*` importing `internal/infra/*` — enforced by review
- `internal/workflow/*` importing `internal/infra/*` — activities are the boundary
- `internal/infra/<a>` importing `internal/infra/<b>` — the eventing package
  (`internal/infra/event/`) is the sole exception: its composer + NatsPublisher
  import `internal/infra/nats`

## Cross-refs

- Plan §2 (repo structure) — [rebuild-plan.md §2](design/rebuild-plan.md#2-repository-structure)
- Plan §3 (schema) — [rebuild-plan.md §3](design/rebuild-plan.md#3-postgres-schema)
- Plan §4 (domain model) — [rebuild-plan.md §4](design/rebuild-plan.md#4-domain-model)
- Plan §9 (adapters) — [rebuild-plan.md §9](design/rebuild-plan.md#adapter-inventory)
- Divergences from this baseline live in [decisions.md](decisions.md)
- Orchestration + workflow ledger: [orchestration.md](./orchestration.md)
- Observability substrate: [observability.md](./observability.md)
- Testing patterns: [testing.md](./testing.md)
