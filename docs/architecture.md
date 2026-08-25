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
│   ├── twitter-auth/main.go             ✓ FF-059: raw-Firefox cookie-capture/status process
│   ├── twitter/main.go                  ✓ T/a+T/b+T/c: real Playwright-Go service (ephemeral profile + idle-CPU prefs)
│   └── worker/main.go                   thin executable; delegates worker composition to internal/app/worker
├── internal/
│   ├── contract/discovery/              stable EventWorkflowInput + CandidateEvidence shared across workflow, activity, and persistence boundaries
│   ├── contract/twittersearch/           ✓ FF-061: one browser/HTTP/activity wire contract for classified search states, evidence, and video refs
│   ├── domain/                          active domain logic only
│   │   ├── fixture/                     ✓ D1: model + State + Repo + tests
│   │   ├── event/                       ✓ D2: model + State + Repo + tests
│   │   ├── video/                       ✓ D3 + V/2 + V/3a + FF-041: model + Repo + rank + versioned perceptual dHash + Match + hard-filter + tests
│   │   ├── alias/                       ✓ canonical-team record + shared text operations; resolver removed 2026-08-16
│   │   ├── team/                        ✓ TrackedTeam set — tracked-teams-cache ingest filter (team.go + repo.go)
│   │   ├── discovery/                   ✓ Query builder + explicit workflow-owned candidate states
│   │   │   ├── doc.go                   Package doc — query construction, URL extraction, source scoring
│   │   │   ├── candidate.go             observed/in-flight/terminal ownership vocabulary
│   │   │   ├── query_builder.go         BuildTwitterQuery, ErrEmptyQuery, ErrEmptyPlayerName (D1/D4b/D4c/D4d/D7 per twitter-search-query.md)
│   │   │   └── query_builder_test.go    name, particle, dedup, fallback, and safeguard cases
│   │   └── vision/                      ✓ D5 (2026-07-28): clock.go + evaluate.go + schema.go + tests — clip-clock validation, wired into EventWorkflow consumer
│   ├── infra/                           live infrastructure adapters
│   │   ├── pg/                          ✓ S2: pool + instruments + schema.sql + VerifySchema drift guard + focused repos + audited exact-selector candidate-replay store
│   │   ├── nats/                        ✓ S3: client + instruments
│   │   ├── s3/                          ✓ S4: Garage client + instruments
│   │   ├── llm/                         ✓ S6: OpenAI-compatible client + typed errors + Chat
│   │   ├── temporal/                    ✓ S5: Client (with workerShutdownTimeout) + Worker
│   │   ├── apifootball/                 ✓ S7 + O1a: /status probe + /fixtures + /fixtures/{ids}
│   │   ├── twitter/                     ✓ classified HTTP Search + forced-Verify client for the Go Twitter service + mock-backed tests
│   │   ├── syndication/                 ✓ S7 + T/f: FetchJSON + ResolveVideo/Download (cookieless mp4) + typed taxonomy + tests
│   │   ├── event/                       ✓ composer (pg event_log audit ONLY — N2 removed its NATS half; Kind = 6 event_log types) · N1+N5 NatsPublisher — 3-subject live-feed (fixture.clock/update, event.video) + Envelope + source config + golden tests
│   │   ├── ffmpeg/                      ✓ V/1 + FF-005: probe + bounded-grayscale single-pass dense extraction + faststart + semaphore + typed taxonomy + tests
│   │   └── firefoxfleet/                ✓ #160 + FF-001: per-event Firefox provisioner via Docker API — Compose-network-scoped daemon names/ownership labels/count/list/reap/release; stable event-only network alias keeps workflow addressing registry-free; idempotent lifecycle + two-fleet/one-daemon tests
│   ├── workflow/                        shipped Temporal workflows
│   │   ├── ingest.go                    ✓ O1c: IngestWorkflow
│   │   ├── active_poll.go               ✓ O2: ActivePollWorkflow (30s IntervalSpec)
│   │   ├── staging_poll.go              ✓ O2: StagingPollWorkflow (*/15 cron)
│   │   ├── twitter_maintenance.go       ✓ FF-058: six-hour static-session persistence + search-DOM canary
│   │   ├── event.go                     ✓ #164c + FF-022 + FF-034 + FF-061: per-goal producer, classified usable/outage budgets, immediate candidate launch, durable recovery
│   │   ├── event_pipeline.go            ✓ shared Selector state, deterministic contexts, restoration, and construction
│   │   ├── event_pipeline_intake.go     ✓ candidate launch, exact-MD5 ownership, hash claimant failover, and consumer loop
│   │   ├── event_pipeline_validation.go ✓ legacy replay path, vision, category-scoped perceptual dedup, and winner selection
│   │   ├── event_pipeline_effects.go    ✓ promotion, supersession, publication, cleanup, and terminal candidate durability
│   │   ├── telemetry.go                 ✓ FF-050: typed replay-aware EventWorkflow lifecycle/search/candidate/publication timing envelope
│   │   └── video.go                     ✓ #165: pre-FF-022 VideoWorkflow child retained for Temporal replay; shared download/hash activity contracts
│   ├── activity/                        activity packages + shared heartbeat helper
│   │   ├── ingest/                      ✓ config, roster, fixture fetch/upsert, canonical-team placeholder, and retention activities
│   │   │   ├── activities.go
│   │   │   ├── tracked_teams.go / fetch.go / categorize.go / aliases.go / retention.go
│   │   │   └── focused colocated test files by responsibility
│   │   ├── monitor/                     ✓ shared deps/config plus activation.go, reconcile.go, emission.go, and event_identity.go; failed-only spawn recovery remains in spawner.go
│   │   ├── discovery/                   ✓ shared config/classified search plus candidates.go durable candidate/search recovery and completion.go checklist closure
│   │   ├── video/                       ✓ DownloadAndStage with bounded failure detail, versioned/minimum-length HashVideo, live-asset recovery, persistence, teardown, and ranking activities
│   │   ├── vision/                      ✓ staged-clip frame extraction + model-backed validation
│   │   ├── fleet/                        ✓ #160: ProvisionFirefox / ReleaseFirefox / ReapOrphanedFirefox / InstanceAddr — thin Temporal-activity wrapper over infra/firefoxfleet; nil-Fleet no-op when fleet disabled (FIREFOXFLEET_ENABLED=false)
│   │   ├── livefeed/                     ✓ publish-activity boundary for all NATS live-feed emits
│   │   ├── twittermaintenance/           ✓ FF-058: forced auth/cookie sync plus minimum-evidence live-search probe
│   │   └── heartbeat/                    shared time-based activity heartbeat loop
│   ├── api/                             ✓ Chi read API over Postgres + S3; no NATS/Temporal dependency; SSE is vedanta-systems'
│   ├── app/worker/                      ✓ worker composition root + Temporal schedule reconciliation; cmd/worker contains no wiring
│   ├── bootstrap/                       ✓ S1 + FF-026 (NOT IN PLAN — see decisions.md 2026-07-07)
│   │   └── bootstrap.go                 Deps + LIFO closer registry; fail-fast metrics/health listener; shared binary lifecycle
│   ├── config/                          ✓ S1 + FF-035: per-binary envconfig profiles, semantic/cross-field validation, and env/Compose contract tests
│   ├── observability/
│   │   ├── vocabulary/                  ✓ S1: typed Module + Action enums
│   │   ├── logging/                     ✓ S1: slog Emit() + TestEmitter for unit tests
│   │   └── metrics/                     ✓ S1: Prometheus registry helper
│   ├── twitter/                         Twitter *service* (browser + auth + scrape); imported by cmd/twitter
│   │   ├── browser.go                   ✓ T/a + FF-017 + FF-061: Firefox context, pre-navigation observers, cookie/session operations, and critical-child exit signal
│   │   ├── browser_iface.go             ✓ T/b: sessionBrowser interface — auth flow testable without Playwright
│   │   ├── stealth.go                   ✓ T/a: navigator.webdriver / plugins / permissions patches
│   │   ├── service.go                   ✓ T/a + T/b + FF-017 + FF-058: state machine, degraded evidence, browser-loss watcher/audit, cookie-operation status, /health, /status
│   │   ├── auth.go                      ✓ T/b + FF-058: warm/forced verification, cookie reload/writeback evidence, /authenticate, /auth/verify
│   │   ├── cookies_backup.go            ✓ T/b + FF-058: full-shape Fingerprint, strict-domain atomic backup, mtime, auth_token guard
│   │   ├── search.go / search_evidence.go ✓ T/c + FF-051 + FF-061: POST /search, bounded DOM/timeline classification, local age cutoff, cancellable scroll/extraction
│   │   └── *_test.go                    cookie, auth, browser-conversion, and search tests
│   ├── twitterauth/                     ✓ FF-059: read-only Firefox SQLite capture, strict publication gate, and health/status service
├── docker/twitter/                      ✓ headless Playwright search image; no VNC packages or runtime branch
├── docker/twitter-auth/                 ✓ FF-059: raw Firefox ESR + Xvfb/noVNC image and container-local supervisor
├── scripts/matchday-status.{sh,sql}     ✓ FF-050/FF-060: environment-scoped, SELECT-only match-day and download-failure snapshot
├── migrations/                          FF-041 operational migration applied to prod; retained with its schema-hash contract until remaining durable environments converge
├── scripts/                             dev smoke/probe programs plus guarded operator tools
│   ├── smoke_repos/main.go              ✓ live pg + repo smoke test (dev only)
│   ├── trigger_ingest/main.go           ✓ live IngestWorkflow trigger (O1d verification)
│   ├── smoke_fleet/main.go              ✓ #160: live per-event fleet smoke — provision→healthy→release one instance (dev only; needs docker.sock + dev network)
│   ├── replay_clock_rejects/main.go      ✓ FF-057: dry-run-first exact clock-reject repair through normal EventWorkflow, sequential and idempotent
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

Fixture, event, video, alias, vision, team, and discovery carry active logic.
Unused session, text-analysis, tracing, use-case, test-helper, and parent
activity placeholders were deleted under FF-045; new packages begin when a
caller and owned behavior exist. The richer packages loosely follow the
layout below (matching
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
API-mirror fields (`APIStatus`, `APIElapsed`, `APIExtra`, scores), derived
nullable winner state, and
domain-managed timestamps (`ActivatedAt`, `TerminalObservedAt`, `CompletedAt`,
`LastActivityAt`, `LastPolledAt`).

State transitions:
- `Activate(at) → active` (sets ActivatedAt, LastActivityAt)
- `Complete(at) → completed` (sets CompletedAt, LastActivityAt)
- `Reschedule(newKickoff, at) → staging` (clears ActivatedAt; for PST/moved fixtures)
- `UpdateFromPoll(status, elapsed, extra, scores, at)` — refreshes API-mirror
  fields and LastPolledAt, starts/preserves a terminal observation, or clears
  it on a successful non-terminal response without changing state
- `UpdatePenalty(home, away)` — exactly mirrors nullable shootout state
- `UpdateResult(providerHome, providerAway)` — derives ordinary/AET winners
  from score and PEN winners from the shootout; exceptional terminal outcomes
  retain exact provider flags ([FF-055 decision](decisions/2026-08-19-winner-state-is-derived-from-canonical-scores.md))

Predicates: `ShouldActivateNow(now, window)` — used by both the ingest
activity (at-upsert-time activation for imminent kickoffs) and the
ActivePollWorkflow's `ActivateUpcoming` step.

Repo methods shipped in `internal/infra/pg/fixture_repo.go`:
`Get`, `Upsert`, `ListByState`, `ListActiveIDs` (cheap ID-only
projection for ActivePollWorkflow's batched API call),
`ListStagingBeforeKickoff`, `AssessCompletion` (terminal grace plus settled
event/downstream gates, returning durable parity audit evidence; see the
[FF-063 decision](decisions/2026-08-25-terminal-observation-grace-bounds-completion.md)),
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

Repo methods ship across the focused `internal/infra/pg/event_repo*.go` files:
`Get`, `GetByNaturalKey`, `Insert(ctx, e, workflowID)` (atomic seed —
`debounce_count=1` + first presence vote for a **known** scorer, but
`debounce_count=0` + **no** vote for an unknown-scorer placeholder, per G1),
`DeleteUnknownEvent` (hard-delete a lingering `debounce_count=0` placeholder),
`UpdateMutableFields`, `Upsert`, `ListPending`, `ListByFixture` (visible rows),
`ListAllByFixture` (FF-027/FF-062 active matching plus removed sequence history),
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
versioned per-frame `frame_hashes` dHash sequence. `FrameHashVersion` includes
algorithm, preprocessing, and sample interval; only equal versions compare.
Storage still enforces only md5 exact-match through `UNIQUE(event_id, md5)`;
the old whole-clip `perceptual_hash` UNIQUE is retired.

Beyond the model, the package owns the dedup + quality logic (pure, table-
tested): `hash.go` (`DHash`/`DHashPNG`), `match.go` (`Match` — the
offset-tolerant sliding window), `filter.go` (`HardFilter` pre-download gate;
the live-calibrated landscape aspect band is 1.73–1.82),
`quality.go` (`IsUpgrade`/`ClipQuality` winner-selection — wired post-vision #171),
and `rank.go` (`CompareShares` — verified, popularity, size, age, then share-ID
total order for deterministic frontend ranks; FF-030).

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
clock parsers with a period-awareness fix. Missing API time is absence of
verification evidence, so soccer footage that passes the content gates enters
the unverified pool instead of being compared against minute zero (FF-031).

- `clock.go` — typed `Period` (`1H`/`2H`/`ET1`/`ET2`) plus structured
  `ClockReading` normalization across conventional continuous clocks,
  reset-per-period clocks, compact stoppage, and frozen-main-clock/sub-timer
  displays. Visible period evidence rebases `05:25 + 2H` to absolute minute 50;
  conflicting or unsupported evidence stays ambiguous instead of becoming a
  confident match. Exact reset/continuous collisions retain both supported
  meanings (`45:xx 2H` → 45/90, `15:xx ET2` → 120/105) without widening the
  tolerance. The stoppage parser accepts both `01:48` and `+1:48` model
  output. `periodOf` remains the fallback for conventional unlabelled clocks.
- `evaluate.go` — `Evaluate(frames, Expected, tol)`: soccer/screen majority
  gates → period-aware clock check → `Outcome` (verified/unverified/rejected).
  API ordinal minutes normalize to the broadcast's completed-minute clock as
  `elapsed + extra - 1` before the ±1 tolerance; the period remains an
  independent guard. An explicit wrong half remains a hard reject; an
  unlabelled reset-clock interpretation can only soft-keep the clip as
  unverified. Strict at halftime / lenient at ET (see decisions.md).
- `schema.go` — `FrameObservation` (per-frame JSON, including nullable visible
  `period`) + `VisionResponse` (`{Frames}`, the `response_format` json-schema,
  exactly-3 positional frames) + `DefaultPrompt`. The model must return period
  only from visible scorebug evidence, never from the clock value alone.

Consumed by `internal/activity/vision.ValidateClip`: fetch staged clip →
`ffmpeg.ExtractFrame` @25/50/75% → one multi-image structured-output vision call
→ `Evaluate`. **Wired into EventWorkflow's consumer** (`event_pipeline*.go`, fired
async per unique clip); the LLM adapter sends `ResponseFormat` plus the public
`reasoning_effort: none` control required by Control's Gemma structured-output
contract. Sampling values remain omitted so the selected model profile owns
their defaults. Clock rejections persist all three raw
frame observations and normalized readings in candidate `outcome_detail`. At
the activity boundary, typed permanent
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
  variant URL without exposing the signed URL in errors (FF-029). Transport,
  invalid-response, and response-stream sentinels let FF-060 persist bounded
  stage/class evidence after retry exhaustion.
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
unreachable fails the current activity call; EventWorkflow's bounded,
one-minute unavailable-probe loop observes recovery without consuming a usable
search (FF-016 + FF-061). Pre-FF-061 histories retain their versioned nested
activity retry policy.

`internal/contract/twittersearch/` owns the request, response, video, bounded
result-state, and secret-free evidence types used on both sides of the HTTP
boundary. The client maps a classified non-2xx response to a typed
`SearchError`; the Discovery activity preserves its page state in a retryable
Temporal application error. New workflows decode those details after one call
and own retry cadence, while pre-FF-061 histories retain their activity retry
chain. Unclassified transport or decode failures remain ordinary activity
errors.

`twitter.Client.Verify(ctx)` targets only the configured static service and
forces `/auth/verify`; `TwitterMaintenanceWorkflow` uses it before its canary
search so quiet periods still verify and persist the shared session (FF-058).

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
- Orchestration + workflow ledger: [orchestration index](./orchestration/)
- Observability substrate: [observability.md](./observability.md)
- Testing patterns: [testing.md](./testing.md)
