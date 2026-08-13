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

## As-shipped tree (2026-08-12, through O3/d + T/c + #160 fleet ship-dark)

```
found-footy/
├── cmd/                                 4 binaries — each imports from internal/
│   ├── api/main.go                      Phase 6 — FastAPI-shaped read surface + SSE
│   ├── scaler/main.go                   scaffold; no scale logic yet (Phase A/M)
│   ├── twitter/main.go                  ✓ T/a+T/b+T/c: real Playwright-Go service (ephemeral profile + idle-CPU prefs)
│   └── worker/main.go                   Temporal worker; registers Ingest + ActivePoll + StagingPoll + Event + Video workflows
├── internal/
│   ├── domain/                          6 shipped, 3 stubbed
│   │   ├── fixture/                     ✓ D1: model + State + Repo + tests
│   │   ├── event/                       ✓ D2: model + State + Repo + tests
│   │   ├── video/                       ✓ D3 + V/2 + V/3a: model + Repo + rank + perceptual dHash + Match + hard-filter + tests
│   │   ├── alias/                       ✓ D4 (reshaped 2026-07-19): two-phase model + Repo + Normalize + Resolver (lookup pipeline) + tests
│   │   ├── team/                        ✓ TrackedTeam set — tracked-teams-cache ingest filter (team.go + repo.go)
│   │   ├── discovery/                   ✓ Query builder (2026-07-22) + real EventWorkflow (O3/d, 2026-07-23)
│   │   │   ├── doc.go                   Package doc — query construction, URL extraction, source scoring
│   │   │   ├── query_builder.go         BuildTwitterQuery, ErrEmptyQuery, ErrEmptyPlayerName (D1/D4b/D4c/D4d/D7 per twitter-search-query.md)
│   │   │   └── query_builder_test.go    18 tests — D8 name table, particles, dedup, fallback, safeguards
│   │   ├── vision/                      ⊘ doc.go stub — build when VideoValidationWorkflow lands (O4)
│   │   ├── session/                     ⊘ doc.go stub — build when Twitter Go service ports (post-O)
│   │   └── textanalysis/                ⊘ doc.go stub — extensibility hook per plan §4
│   ├── infra/                           13 live
│   │   ├── pg/                          ✓ S2: pool + instruments + schema.sql + FixtureRepo + EventRepo + AliasRepo + TeamRepo + AssetRepo/ShareRepo (#164a)
│   │   ├── nats/                        ✓ S3: client + instruments
│   │   ├── s3/                          ✓ S4: Garage client + instruments
│   │   ├── llm/                         ✓ S6: OpenAI-compatible client + typed errors + Chat
│   │   ├── temporal/                    ✓ S5: Client (with workerShutdownTimeout) + Worker
│   │   ├── apifootball/                 ✓ S7 + O1a: /status probe + /fixtures + /fixtures/{ids}
│   │   ├── twitter/                     ✓ S7: HTTP client + tests against mock (real service is Python)
│   │   ├── syndication/                 ✓ S7 + T/f: FetchJSON + ResolveVideo/Download (cookieless mp4) + typed taxonomy + tests
│   │   ├── wikidata/                    ✓ S7: SPARQL client + tests
│   │   ├── wikipedia/                ✓ S7: CirrusSearch entity resolution (per 2026-07-21) + tests
│   │   ├── event/                       ✓ O3/a: dual-write composer (pg event_log + NATS Publish, 6 kinds) + tests
│   │   ├── ffmpeg/                      ✓ V/1: probe + single/dense frame extract (single-pass fps) + faststart + semaphore + typed taxonomy + tests
│   │   └── firefoxfleet/                ✓ #160 (ship-dark, FIREFOXFLEET_ENABLED=false): per-event Firefox provisioner via Docker API — deterministic name/addr (no registry), idempotent Provision/Release, running-only label-counted cap, ListInstances/ReapOrphans reaper (audit P0-5) + tests
│   ├── workflow/                        6 shipped
│   │   ├── ingest.go                    ✓ O1c: IngestWorkflow
│   │   ├── active_poll.go               ✓ O2: ActivePollWorkflow (30s IntervalSpec)
│   │   ├── staging_poll.go              ✓ O2: StagingPollWorkflow (*/15 cron)
│   │   ├── event.go                     ✓ #164c: EventWorkflow — per-goal orchestrator (producer: discovery search + spawn Video children; ex-DiscoveryWorkflow)
│   │   ├── event_pipeline.go            ✓ #164c-b + #171: Selector consumer — md5 gate → vision → category-scoped perceptual dedup + IsUpgrade winner-select → promote/supersede → rank; assets/pending/inFlight state; searchDone&&inFlight==0 completion
│   │   └── video.go                     ✓ #165: VideoWorkflow child (download → hash)
│   ├── activity/                        5 packages shipped
│   │   ├── ingest/                      ✓ O1b: 4 activities + in-memory fakes + 11 tests
│   │   │   ├── activities.go
│   │   │   └── activities_test.go
│   │   ├── monitor/                     ✓ O2a: 6 activities (GetMonitorConfig, ActivateUpcoming, PollStagingFixtures, ListActiveFixtureIDs, FetchLiveFixtures, ReconcileFixture) + fakes + tests
│   │   ├── discovery/                   ✓ O3/d: GetDiscoveryConfig, FetchTeamAliases, SearchTweets, StoreCandidate, MarkDownstreamComplete (no _test.go yet — audit gap)
│   │   ├── video/                       ✓ V/3b: DownloadAndStage + HashVideo (staging-split + pre-download filter) + fakes + 8 tests
│   │   └── fleet/                        ✓ #160: ProvisionFirefox / ReleaseFirefox / ReapOrphanedFirefox / InstanceAddr — thin Temporal-activity wrapper over infra/firefoxfleet; nil-Fleet no-op when ship-dark
│   ├── api/                             ✓ #167: Chi read API — GET /fixtures(+?ids batch) /fixtures/{id} /events?ids /events/{id} /videos/{share_id}(302→presign chain); dto.go + handlers.go + router.go + tests; SSE is vedanta-systems'
│   ├── bootstrap/                       ✓ S1 (NOT IN PLAN — see decisions.md 2026-07-07)
│   │   └── bootstrap.go                 Deps + LIFO Closer registry; shared binary startup
│   ├── config/                          ✓ S1: envconfig-based Config with per-adapter sub-structs
│   ├── observability/
│   │   ├── vocabulary/                  ✓ S1: typed Module + Action enums
│   │   ├── logging/                     ✓ S1: slog Emit() + TestEmitter for unit tests
│   │   ├── metrics/                     ✓ S1: Prometheus registry helper
│   │   └── tracing/                     ⊘ stub (Noop tracer, ~22 lines; real OTLP Phase 5+)
│   ├── scaler/                          scaffold; no logic (Phase A/M)
│   ├── testutil/                        ⊘ empty (build as testing needs surface)
│   ├── twitter/                         Twitter *service* (browser + auth + scrape); imported by cmd/twitter
│   │   ├── browser.go                   ✓ T/a: Playwright-Go + Firefox persistent context, GetCookies + ReplaceCookies + LoadCookies + VerifySession
│   │   ├── browser_iface.go             ✓ T/b: sessionBrowser interface — auth flow testable without Playwright
│   │   ├── stealth.go                   ✓ T/a: navigator.webdriver / plugins / permissions patches
│   │   ├── service.go                   ✓ T/a + T/b: state machine (starting/loading/healthy/unauthenticated/failed), /health, /status
│   │   ├── auth.go                      ✓ T/b: EnsureAuthenticated (mtime → warm-path → verify) + BackupCookies + /authenticate + /auth/verify
│   │   ├── cookies_backup.go            ✓ T/b: Fingerprint, WriteBackup (atomic), ReadBackup, BackupFileMtime, auth_token guard
│   │   ├── search.go                    ✓ T/c: POST /search + full DOM scrape + 4-condition scroll loop + BackupCookies hook + combined verify+search + stealth jitter
│   │   └── *_test.go                    40 unit tests (10 cookie backup + 16 auth flow + 12 search + 2 more from T/b.5)
│   └── usecases/                        ⊘ doc.go stub (build when cross-domain ops surface)
├── docker/twitter/                      ✓ T/b: twitter service image + entrypoint (peer of internal/)
│   ├── Dockerfile                       Playwright base + playwright-go driver + optional WITH_VNC layer (~150 MB xvfb+fluxbox+x11vnc+novnc+websockify)
│   └── entrypoint.sh                    Conditionally boots VNC daemon stack when TWITTER_VNC_MODE=true, otherwise passthrough
├── migrations/                          ⊘ EMPTY — schema.sql lives in internal/infra/pg/ instead
│                                          (see decisions.md 2026-07-07)
├── scripts/                             smoke + trigger scripts
│   ├── smoke_repos/main.go              ✓ live pg + repo smoke test (dev only)
│   ├── trigger_ingest/main.go           ✓ live IngestWorkflow trigger (O1d verification)
│   └── smoke_fleet/main.go              ✓ #160: live per-event fleet smoke — provision→healthy→release one instance (dev only; needs docker.sock + dev network)
├── test/                                ✓ scenario harness (Phase T shipped early)
│   ├── harness/                         ✓ testcontainer pg + mock apifootball + assertion engine
│   ├── scenarios/                       ✓ YAML corpus organized by suite
│   │   ├── basic/                       ✓ happy paths
│   │   ├── debounce/                    ⊘ pending Monitor implementation
│   │   ├── faults/                      ⊘ pending
│   │   ├── edge_cases/                  ⊘ pending
│   │   └── regression/                  ⊘ pending
│   └── scenarios_test.go                ✓ corpus runner (iterates YAML files)
├── caddy/found-footy.caddy              routing stubs; not yet copied into ~/workspace/proxy/caddy.d/
├── docker-compose.dev.yml               ✓ dev stack; air hot-reload on all 4 Go binaries
├── docker-compose.prod.yml              runs PYTHON codebase; unchanged (name reflects intent)
├── Dockerfile / Dockerfile.dev          ✓ multi-stage prod + air-based dev
├── go.mod / go.sum                      ✓ Go 1.25 (bumped from 1.23 for air compat)
├── Makefile                             ✓ build/test/test-short via docker run
└── docs/                                see docs/README.md for routing
```

Legend:
- `✓ <phase>` — shipped in that phase, has real code + tests
- `⊘` — stubbed (usually a `doc.go` marker), waiting for its dependent phase
- No marker — not part of the rebuild (Python-era or config)

## Domain packages — as-shipped shape

Nine domain packages: **6 with substantial logic** — fixture, event, video,
alias, vision, team — plus discovery (query builder) and session + textanalysis
(stubs). The richer ones loosely follow the layout below (matching
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
evaluator, see [completion-contract.md](design/proposals/completion-contract.md)),
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
`Upsert`, `ListPending`, `EventsAwaitingDiscovery` (the discovery spawn set),
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

Reshaped 2026-07-19 for the deterministic (no-LLM) Wikidata pipeline;
see [decisions.md 2026-07-19](decisions.md) and
[proposals/team-aliases.md](design/proposals/team-aliases.md).

Core type `alias.TeamAlias`. Two-phase fields:

- Phase-1 (vendor, Ingest-populated): `team_id`, `canonical_name`,
  `team_code`, `country`, `city`, `is_national`.
- Phase-2 (resolution-populated): `wikidata_qid`, `aliases`, `resolved_at`.

Predicates: `IsResolved()`, `IsFresh(now, ttl)` — the 30-day TTL check
runs at pipeline read-time before Discovery consumes aliases.

Setter: `SetResolution(qid, aliases, at)` writes all three phase-2
fields atomically + copies the aliases slice defensively.

Normalize helper: NFD Latin-diacritic strip, preserved case. Exported for
the (future) Twitter search-query builder — **currently no production
caller** (tests only).

Repo methods shipped: `Get`, `BulkGet`, `UpsertVendorFields`,
`UpsertResolution`. The Upsert split enforces the invariant that
Ingest's daily vendor-refresh CANNOT wipe an existing resolution —
`UpsertVendorFields` writes only phase-1 columns via ON CONFLICT DO
UPDATE. `UpsertResolution` writes both phases and is the only entry
point for the (upcoming task #134) resolution activity.

**Selection pipeline (2026-07-20, task #134):** The QID → `[]string`
aliases step. `Resolver.Select` fetches the team entity, extracts
multilingual aliases + labels (11 Latin-script langs: en/es/fr/it/pt/de/ca/gl/nl/pl/ro) + P1449 nicknames + canonical name tokens, runs
the keep rule (≥2 langs OR P1449 OR canonical), rescues single-lang
English aliases (LFC, CFC, MCFC), and — for clubs — drops the venue
city token if it's not in the canonical name (Arsenal ≠ London;
Liverpool ✓ Liverpool). Nationals additionally fetch the linked
country entity (via P17) and inject English P1549 demonyms
(Argentina → Argentine + Argentinian + Argentinean).

Skip-list (`skiplist.go`) drops pure organizational descriptors +
articles across 11 languages. Explicit "never skip" carve-outs for
team-identifying words that look generic — `united, city, athletic,
sporting, real, rangers, rovers, borussia, juventus, elftal,
mannschaft, seleção, seleccion, oranje, azzurri` — so Python's LLM
over-filtering behavior doesn't recur.

Output is sorted for stable pg storage + human-diffable rows.

**Ingest wiring (2026-07-20, task #134):** New activity
`ResolveAliasesForTeams` runs after `EnsureAliasPlaceholders` in
`IngestWorkflow`. Per-team loop: `BulkGet` existing rows → skip
cache-hits (row has `wikidata_qid` set) → for each miss,
`AliasResolver.Resolve` (fuzzy lookup) → `AliasResolver.Select`
(entity fetch + selection) → `AliasRepo.UpsertResolution`. Sequential
with 500ms throttle between teams (belt-and-braces against vendor
rate limits — Wikipedia's CirrusSearch handles 200 req/s per IP,
Wikidata SPARQL is friendlier, but the throttle keeps a runaway
loop from ever tripping either). Soft-fail
per team — a Wikidata hiccup leaves the placeholder row and gets
retried on the next Ingest cycle. Mirrors Python's per-day-fixture-
team pattern (workflow.execute_activity in a loop; not all tracked
teams at once). Output counts: cache_hits, resolved, no_match, failed.

**Per-team API-Football enrichment (2026-07-20, task #142):** Each
cache-miss team in `ResolveAliasesForTeams` first calls the new
`apifootball.GetTeamProfile(teamID)` — one `GET /teams?id=X` per
team. Returns `venue.city` (native-language, e.g. "Milano" not
"Milan"), authoritative `team.country` (works for friendlies where
`league.country == "World"`), `team.national` (source of truth), and
`team.code` (3-letter FIFA/UEFA). Enriched vendor fields are upserted
back to `team_aliases` immediately via `UpsertVendorFields` so the
row is captured even when Wikidata resolution fails downstream. Then
the enriched values (city especially — decisive for the club-branch
scoring's short-circuit) get passed into `alias.LookupInput` for the
Wikidata pipeline. Ports Python's `get_team_info` call in
`archive/src/activities/rag.py:555`. Cost: 1 API-Football call per
team lifetime (cache-hits skip both this call and Wikidata). Soft-fail
per team — profile fetch error keeps the TeamRef fallback values.

**Lookup pipeline (2026-07-21, task #147):** The name → Wikidata QID
resolution step. `Resolver` composes a `WikipediaResolver` interface
(CirrusSearch full-text candidate generation) with a
`WikidataFetcher` interface (P31 verification + alias extraction) —
both injected so the domain stays pure Go. Two branches on
`LookupInput.IsNational`:

- Clubs (`lookup_club.go`): ONE Wikipedia CirrusSearch query with
  template `{name} {country} football club` → hits with Wikidata
  QIDs (extracted from `pageprops.wikibase_item`) → ONE SPARQL P31
  batch verify against Wikidata's ontology (accept: Q476028
  association football club, Q103229495 men's association football
  team; reject: Q2412834 reserve team, Q51481377 women's football
  club) → first Wikipedia-ranked survivor wins. 2 HTTP calls per
  cache-miss team.
- Nationals (`lookup_national.go`): same shape, query is `{country}
  men's national football team` (Wikipedia's article-title convention
  makes this near-deterministic). P31 accept set adds Q135408445
  men's national football team and legacy Q6979593; reject Q6997908
  women's national. USA substituted to "United States" per Wikipedia
  article-title convention.

Fallback: on SPARQL failure the resolver takes Wikipedia's top hit
unconditionally rather than cascading to NoMatch. CirrusSearch's
BM25 + field-boosted ranking is generally good enough that even
without type verification the top hit is right; graceful degradation
beats a whole-roster miss during vendor blips.

`ErrNoMatch` when Wikipedia returns zero hits (or zero with valid
`wikibase_item`) — legitimate for obscure teams not on Wikipedia,
not a bug. In practice 38 of 38 tracked-league teams resolve on the
2026-04-26 dev roster.

**P31 batch verification (2026-07-20, task #143):** `wikidata.BatchGetP31([qids])`
sends ONE SPARQL query (`SELECT ?item ?type WHERE { VALUES ?item { … } ?item wdt:P31 ?type }`)
and returns QID → P31 type list. Structural type check replaces the
earlier text heuristics — TV channels, stadiums, museums, disambiguation
pages, supporters' associations, match instances all get dropped even
when their descriptions happen to contain "football".

**Why Wikipedia + Wikidata split (design ref
[`proposals/alias-entity-resolution.md`](design/proposals/alias-entity-resolution.md)):**
Wikidata's `wbsearchentities` is a label + alias prefix index — misses
entities whose mention doesn't share a prefix with the canonical label
(Nice ≠ OGC Nice's prefix). Wikipedia's CirrusSearch (ElasticSearch
BM25 over article body) finds them via context-augmented queries.
Wikipedia articles carry `pageprops.wikibase_item` → the Wikidata QID
bridges cleanly back to the structured KB for aliases, P31, and
demonyms. **Wikipedia is the entity resolver; Wikidata is the alias
source.**

Adapter surface consumed by the Resolver:
- `wikipedia.SearchAndResolve(query, opts)` — one HTTP round-trip
  (`list=search` + `generator=search` + `prop=pageprops` composed)
  returning `[]Hit{Title, WikidataQID, Index}`. `internal/infra/wikipedia/`.
- `wikidata.BatchGetP31(qids)` — one SPARQL query per Resolve.
- `wikidata.GetEntity(qid)` — used by the SELECT phase for alias
  extraction (unchanged from earlier).

Shared word-processing in `text.go`: NFD normalize, strip diacritics,
`ß→ss`, split on whitespace/dashes/slashes, strip periods/commas/
apostrophes, lowercase, drop ≤2 char / all-digit / CamelCase-concat.

Integration test in `lookup_integration_test.go` verifies real
Wikipedia + Wikidata resolutions for 4 teams (Liverpool, Man United,
France, Brazil) — skipped in `-short`.

### vision domain (D5) — shipped 2026-07-28

Clip-validation logic, pure + table-tested (no I/O, no model). Ports the Python
clock parsers with a period-awareness fix.

- `clock.go` — scorebug field parsers (`parseClockField`, `parseAddedField`,
  `parseStoppageClockField` — the last strips a leading `+`, since gemma returns
  `01:48` and Qwen `+1:48`) + `periodOf` (the H1/H2/ET1/ET2 map, verified against
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
fields (rung 1) exist for this call.

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
  (ErrRateLimited, ErrCapExceeded, etc.).
- **apifootball**: getJSON helper handles auth (`x-apisports-key` per
  doc) + rate-limit-header parsing (per-minute + daily distinct) +
  error classification. `/fixtures` (single + by-IDs) landed in O1a.
  `ListFixturesByIDs` accepts any-size input, chunks internally at
  `IDsBatchLimit=20` (exported const, sourced from vendor doc), fires
  per-chunk HTTP calls in parallel via `errgroup`, returns
  `(fixtures, failedIDs, err)`. Partial failure surfaces as non-empty
  `failedIDs`. See decisions.md 2026-07-09 refactor entry.

**Twitter service note.** `internal/infra/twitter/` is the HTTP client;
tests pass against a mock. The actual twitter container in dev runs
the real Go Twitter search service (T/a+T/b+T/c shipped 2026-07-23; Python
`twitter/` in prod). Wire-up deferred until the Go twitter service
lands.

`twitter.Client.Search(ctx, addr, req)` takes a **per-call base address**
(#160): empty `addr` → the shared `TwitterConfig.BaseURL` (pre-#160
behavior); a non-empty `addr` → that event's dedicated fleet instance,
derived by `firefoxfleet.InstanceAddr(eventID)`. The EventWorkflow decides
which by `FleetEnabled` and threads it through `SearchTweetsInput.InstanceAddr`.
This is how one HTTP client fans searches across N per-event Firefox
containers without a router or registry — the address is a pure function
of the event ID.

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
- `internal/infra/<a>` importing `internal/infra/<b>` — one composer package
  (`internal/infra/event/`, when built) is the sole exception

## Cross-refs

- Plan §2 (repo structure) — [rebuild-plan.md §2](design/rebuild-plan.md#2-repository-structure)
- Plan §3 (schema) — [rebuild-plan.md §3](design/rebuild-plan.md#3-postgres-schema)
- Plan §4 (domain model) — [rebuild-plan.md §4](design/rebuild-plan.md#4-domain-model)
- Plan §9 (adapters) — [rebuild-plan.md §9](design/rebuild-plan.md#adapter-inventory)
- Divergences from this baseline live in [decisions.md](decisions.md)
- Orchestration + workflow ledger: [orchestration.md](./orchestration.md)
- Observability substrate: [observability.md](./observability.md)
- Testing patterns: [testing.md](./testing.md)
