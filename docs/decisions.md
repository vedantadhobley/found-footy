# Architectural Decisions

Append-only. Newest at top. Dates in `YYYY-MM-DD`. When migrating an old
decision because the rationale changed, leave the old entry in place and
add a new one above it pointing at the change.

---

## 2026-07-07 — Working rule: living docs update in the same commit as code

Retrospective response to a stretch of ~15 commits (S1–O1d) where code
shipped without corresponding updates to `docs/rebuild/*.md`.

**Rule.** Every implementation commit that adds/changes a package,
adapter shape, workflow, or activity MUST update the relevant living
doc (usually `docs/rebuild/architecture.md` or
`docs/rebuild/orchestration.md`) in the SAME commit. If it diverges
from `docs/rebuild-plan.md`, `docs/decisions.md` gets an entry.

**Why.** The plan is intent — 12k lines, written before implementation,
not per-commit. The per-topic docs in `docs/rebuild/` were meant to be
the ledger of what actually shipped and are currently empty stubs.
Silent code-only commits meant the plan drifted from reality without
either doc surfacing that fact. This retro compensates. Going forward,
the rule prevents recurrence.

**Enforcement.** An implementation commit without a corresponding doc
update is treated as incomplete — same status as one missing tests.

---

## 2026-07-07 — Temporal adapter divergences from plan §9

Three divergences between `internal/infra/temporal/` (shipped in S5)
and plan §9's temporal spec. Retrospectively logged as part of the
doc retro.

**1. `NewClient` takes `*Instruments`, not `*slog.Logger`.** Plan §9
had `func NewClient(ctx, cfg, logger *slog.Logger) (client.Client, error)`.
Shipped: `func NewClient(ctx, cfg, ins *Instruments) (*Client, error)`.
**Silent.** Instruments carry logger + metrics + (eventually) tracing
together; the plan's logger-only param would be an outlier vs every
other adapter (S2+). **Keep — consistent with adapter template.**

**2. `Client` wraps SDK type; doesn't return raw `client.Client`.**
Plan §9 wanted `(client.Client, error)` return. Shipped: `(*Client,
error)` with our type. **Silent.** Rationale: (a) `WorkerShutdownTimeout()`
accessor for graceful shutdown ordering, (b) our `Close()` hook can
emit metrics, (c) tracing hookup later without changing callers.
**Keep.**

**3. `worker.Options` param on `NewWorker`; no hardcoded defaults.**
Plan §9 wanted `NewWorker(c, cfg, logger)` returning a worker with
"sensible defaults" (MaxConcurrentActivityExecutions:30,
MaxConcurrentWorkflowTasks:10) baked in. Shipped:
`NewWorker(c, ins, options worker.Options)` with caller-supplied
options. **Silent.** Rationale: cmd binaries know what they're
running; adapter shouldn't decide concurrency. **Keep.**

**4. `DefaultRetryPolicy()` helper not shipped.** Plan §9 called for
`func DefaultRetryPolicy() *temporal.RetryPolicy`. Not implemented.
Each workflow defines its own `workflow.ActivityOptions` inline
(e.g. `internal/workflow/ingest.go`). Rationale: retry policy visible
at call site is easier to audit than a shared default that hides its
config. **Keep the divergence; delete the plan's helper on next plan
revision.**

**5. `SignalWorkflow` method added on Client.** Not in plan §9.
Sensible addition; kept for future AssetPersistenceWorkflow signal
paths.

---

## 2026-07-07 — Log-catalog generator (§11.3) not shipped

Plan §11.3 specifies a generated `docs/generated/log-catalog.md`
regenerated on every build via `go generate`, listing every
(vocabulary.Module, vocabulary.Action) pair with expected field set
and log-level guidance.

**Status:** not implemented in S1 or since.

**Rationale for the deferral:** with ~50 declared actions across
11 adapter modules + 5 workflow modules today, `grep -r "vocabulary\.Action"`
is still ergonomic. Generator adds a build-time dependency (needs a
tool + a generate marker + a CI step to keep the checked-in catalog
current) that's cheap to build later when the surface grows and hard
to justify now.

**Trigger to build:** when the vocabulary passes ~200 declared
actions OR when someone asks "what actions does X module emit?" and
grep isn't fast enough OR when we introduce webhook consumers that
need a discoverable catalog to subscribe to.

**Not a design change.** The plan's shape (per-(module, action)
markdown table) still stands; only the timing shifts.

---

## 2026-07-07 — IngestWorkflow divergences from plan §5 W1

Six divergences between the shipped IngestWorkflow (O1a-O1d) and the
plan's §5 W1 spec. Retrospectively logged as part of the
2026-07-07 doc retro. Three are user-approved conversationally,
three were silent implementation choices — decisions on whether to
keep or realign made below.

**1. Input shape.** Plan wanted:
```go
type IngestWorkflowInput struct {
    ManualDate       *time.Time   // nil for scheduled; set for manual re-run
    ManualFixtureIDs []int64      // nil for full-window; set for ad-hoc re-ingest
    RetentionDays    int          // default 14
}
```
Shipped:
```go
type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration
    RetentionThreshold time.Time
}
```
**Silent.** My choice, not discussed. Consequences: no ad-hoc
per-ID re-ingest (already bit me — `scripts/trigger_ingest`
hardcodes a window because `ManualFixtureIDs` doesn't exist).
Explicit window params are more general but every caller (schedule +
manual trigger) has to compute them; plan's `ManualDate` +
schedule-default was simpler for the common case.
**Decision:** realign to plan shape in **O1e follow-up commit**.
Add back `ManualFixtureIDs` for ad-hoc re-ingest. Keep
`ActivationWindow` as an addition (see #6). Convert
`RetentionThreshold time.Time` back to `RetentionDays int`
(schedule computes cutoff from days).

**2. Output shape.** Plan wanted:
```go
type IngestWorkflowOutput struct {
    FixturesUpserted int
    AliasesCached    int
    FixturesPruned   int
    Errors           []string  // non-fatal errors surfaced for observability
}
```
Shipped: 9 int counters (staging, active, completed, existing,
inserted, aliasErrors, categorizeErrors, prunedFixtures, fetched).
No `Errors []string`.
**Silent.** More granular but loses the observability signal for
non-fatal per-fixture errors that don't fail the workflow.
**Decision:** realign in O1e. Keep the granular counts as fields
alongside `Errors []string`. Both are useful.

**3. `ListUniqueTeamsFromFixtures` inlined into
`CategorizeAndUpsertFixtures`.** Plan had it as a separate activity
step. Shipped: collected via a `map[int]TeamRef` inside the
categorize loop, returned as part of `CategorizeOutput.TeamRefs`.
**Silent.** Rationale for the choice: dedup-by-team-id happens in
memory; extracting it to a separate activity means re-marshaling
the full `[]APIFixture` across the Temporal activity boundary for
what's effectively a pure map-reduce.
**Decision:** keep. Defensible improvement over the plan.
Documented in `docs/rebuild/orchestration.md`.

**4. `PreCacheAliasesBatch` → `EnsureAliasPlaceholders`.** Plan
had `PreCacheAliasesBatch` doing full RAG resolution (Wikidata
lookup + LLM twitter-alias inference) inline during ingest.
Shipped: `EnsureAliasPlaceholders` inserts blank-resolution
placeholder rows; a separate resolution job (design TBD) fills
them later.
**User-approved conversationally.** Rationale: (a) RAG on hot
ingest couples the daily job to joi + Wikidata availability; (b)
the RAG design itself needs revisiting (team-alias + player-name
search-string building could be improved over Python's approach);
(c) user explicitly said "don't just port Python's logic here."
**Decision:** keep. Retroactively documented.

**5. No `ManualFixtureIDs` support.** Direct consequence of #1.
Plan had it as first-class input for ad-hoc re-ingest of specific
fixtures.
**Decision:** realign as part of #1 fix.

**6. `ActivationWindow` parameter added — not in plan.**
Introduced to fix the Python-era "manual ingest at 14:55 for 15:00
kickoff sits in staging until next monitor cycle" bug (see
[2026-07-07 Fixture activation triggers](#2026-07-07--fixture-activation-triggers--staging-poll-design)).
**User-approved conversationally.** Designed together during
Phase D.
**Decision:** keep. Retroactively documented.

---

## 2026-07-07 — Rebuild architecture divergences from plan §2

Documented here as part of the retrospective (see rule above). Most of
these emerged silently during S1–S7 and D and only got captured after
the fact.

**Divergences from `rebuild-plan.md` §2 tree:**

1. **`migrations/` is empty; schema lives in
   `internal/infra/pg/schema.sql`.** Plan wanted golang-migrate style
   SQL files at repo root. Instead, we ship a single `schema.sql` that
   docker-entrypoint-initdb.d mounts into dev postgres (fresh volume
   only) and testcontainers loads via `WithInitScripts`. Rationale:
   pg pool + schema landed in S2; no migrations tooling was needed
   because dev + test rebuild the DB from scratch. Prod migration
   tooling is a Phase M concern (not yet designed).
   **Decision:** keep `internal/infra/pg/schema.sql` as the
   authoritative source through Phase M. Introduce golang-migrate (or
   equivalent) alongside the Phase M cutover plan.

2. **`internal/bootstrap/` package added — not in the plan tree.**
   Contains `bootstrap.Run(...)` (shared binary startup: config load,
   signal wiring, metrics server, LIFO Closer registry) + `Deps`
   struct passed to each cmd's main. Introduced in S1 to eliminate
   ~50 lines of boilerplate per binary and centralize graceful-shutdown
   ordering. **Decision:** keep — the LIFO Closer registry pattern is
   load-bearing for temporal worker draining (worker Stop must run
   before pg pool Close).

3. **`internal/infra/event/` composer stubbed** (only `doc.go`).
   Plan wants the semantic-event dual-write composer (pg + nats) here.
   Deferred to Phase O2 when MonitorWorkflow starts emitting
   `event.detected` / `event.stable` / `event.removed`.

4. **`internal/infra/ffmpeg/` stubbed** (only `doc.go`). Deferred to
   Phase A (video pipeline).

5. **`internal/usecases/` stubbed** (only `doc.go`). Deferred to when
   the first cross-domain operation surfaces (probably VAR removal
   during O2/O3).

6. **`internal/testutil/` empty.** Build factories/fakes when second
   activity or workflow package needs sharing. Currently the ingest
   activity's fakes are inlined in its test file — cost of premature
   extraction > cost of duplication at N=1.

7. **Domain packages `discovery`, `vision`, `session`, `textanalysis`
   stubbed** (only `doc.go` each). Explicit conversation-approved
   deferral: build each when the corresponding workflow needs it.
   Rationale — building all 8 domain packages up front risks
   speculative modeling; building on demand keeps the shapes tight to
   real callers.

**No divergence** from plan §2 tree for: `cmd/`, `internal/domain/`
(the shipped 4), `internal/workflow/`, `internal/activity/`,
`internal/api/`, `internal/config/`, `internal/errors/`,
`internal/observability/`, `internal/scaler/`, adapter tree (except
the two composer stubs above), `caddy/`, Dockerfiles, Makefile.

---

## 2026-07-07 — Fixture activation triggers + staging-poll design

**Fixture activation** (staging → active) fires from three triggers,
all routed through the same `Fixture.Activate(at)` primitive:

1. **Ingest-time pre-activation.** At upsert time, if
   `f.ShouldActivateNow(now, 30min)` is true, the ingest activity
   calls Activate BEFORE the first `repo.Upsert`. The fixture never
   lands in staging in the DB. Fixes the 2026-05 Python-era bug
   where manual ingest at 14:55 for a 15:00 kickoff sat in staging
   until the next 15-min monitor cycle.
2. **Monitor pre-activation.** `MonitorWorkflow`'s
   `PreActivateUpcoming(30min)` activity scans staging fixtures every
   30 seconds and promotes any that ShouldActivateNow returns true
   for. Uses the same helper as (1) so the rule stays one place.
3. **Emergency activation.** If the monitor's API response for a
   staging fixture shows `APIStatus.Live()`, promote immediately
   (already-live match wasn't caught by pre-activation because
   ingest had the wrong kickoff or the API published a corrected
   one). Same `Activate(now)` transition.

**Staging-poll design.** The plan §5.2 as-drafted skipped the
Python-era 15-minute staging API poll, meaning postponements +
kickoff changes on staging fixtures would go undetected until the
fixture activated (which may never happen if the new kickoff is far
out). This was already noted in [`docs/todo.md`](./todo.md) as a
deferred Go-rebuild item after the 2026-07-05 Mexico vs England
postponement surfaced the gap.

Decision for Phase O's MonitorWorkflow: **mirror Python's approach.**
Every 30-second cycle:

- Always: `PreActivateUpcoming(30min)` (DB-only check, no API call)
- Always: fetch API status for active fixtures
- **On 15-min aligned boundaries** (`:00 / :15 / :30 / :45`): also
  fetch API status for staging fixtures whose `LastPolledAt` doesn't
  match the current 15-min interval. Detects postponements,
  kickoff changes, status-flipped-to-live. Per-fixture skip keeps
  the total API burn near-zero when nothing's changing.

The alignment logic is a monitor-workflow concern (schedule decision,
not domain), so it doesn't touch the fixture domain package. What
domain does provide: `ShouldActivateNow`, `APIStatus.Live()`,
`Activate`. Those three primitives compose to cover all three
triggers.

## 2026-07-07 — Workflow renames for Phase O

The Go rebuild renames three of the six Python-era Temporal workflows.
Nothing runs under the Go names yet — workflow IDs live in Temporal
history permanently, so renaming after Phase O ships would leave a
mixed vocabulary in perpetuity. Renaming now = free.

| Python name         | Go rebuild name              | Reason |
|---------------------|------------------------------|--------|
| `IngestWorkflow`    | `IngestWorkflow`             | unchanged — does exactly what it says |
| `MonitorWorkflow`   | `MonitorWorkflow`            | unchanged — poll active fixtures every 30s |
| `TwitterWorkflow`   | **`DiscoveryWorkflow`**      | "Twitter" describes the source; the workflow's job is discovering candidate videos. Rename lets a future YouTube/TikTok/Reddit source fold in without a rename cascade. |
| `DownloadWorkflow`  | **`VideoValidationWorkflow`** | downloads + AI-validates + hashes. "Download" undersells what the workflow does. |
| `UploadWorkflow`    | **`AssetPersistenceWorkflow`** | dedups first, THEN maybe uploads. The dedup is the load-bearing part; "Upload" hides it. |
| `RAGWorkflow`       | **(folded into `IngestWorkflow` as a sub-activity)** | "RAG" is implementation vocabulary. Alias resolution is one step of ingest, not a separate top-level workflow. |

Naming for Temporal task types + workflow IDs follows the new names.

## 2026-07-02 — NATS is metadata-plane only; video bytes go over HTTP

**Decision:** NATS/JetStream carries semantic events, SSE fan-out, and
webhook delivery — all small structured messages. Video bytes are NEVER
routed through NATS. Browsers fetch videos via HTTP/HTTPS directly from
Garage (S3-compatible) through the workspace Caddy, using presigned URLs
returned by the `/api/v1/videos/{share_id}` 302 redirect.

**Why NATS is wrong for video byte transport:**

- **Broker memory + storage blowup**: even at 20 MB per clip and hundreds
  of clips per match day, pushing videos through NATS turns the
  workspace-shared broker into a media store. Wrong tool for the shape.
- **No range-request support**: browsers need `Range: bytes=x-y` to seek
  within a video. HTTP/S3 does this natively; NATS doesn't.
- **Poor latency**: NATS adds a hop the CDN model avoids. Playback
  needs the shortest possible path from bytes-on-disk to browser
  decoder.
- **Wrong caching model**: Caddy + Garage can add edge caching, ETags,
  and Cache-Control; NATS as a message broker doesn't have these
  primitives.

The common misconception is that "large streaming services use Kafka
for video." They don't. Netflix / YouTube / Twitch use Kafka (or
equivalent) for the **metadata plane** — view counts, recommendation
events, ad tracking, telemetry. Bytes always go over HTTP-based
segment protocols (HLS, MPEG-DASH) backed by S3-alike storage + CDN.
Kafka is not in the byte path.

**Why the play-latency problem ("video sometimes needs close+reload
before playing") has a different root cause:**

Almost certainly one or more of:

1. **Video not encoded with `-movflags +faststart`**: MP4's index (moov
   atom) defaults to the END of the file. Browsers can't start playing
   until they've downloaded the whole file. `-movflags +faststart`
   moves the moov atom to the front and the browser starts playing
   after ~500 KB. Biggest single win.
2. **Slow presigned URL generation**: Python's boto3 signing is not
   fast; Go's `aws-sdk-go-v2` signing is significantly faster. Free
   win from the rebuild.
3. **First-byte latency from cold MinIO**: Garage has better cache
   locality by default.
4. **Redirect chain adds RTT**: `Cache-Control` on the 302 lets the
   browser cache the target URL and skip the API round-trip on repeat.

**What ships in the new stack to fix play latency:**

- **`-movflags +faststart` on every downloaded video.** The
  `DownloadVideo` activity (§7) invokes ffmpeg with this flag as part
  of its download/normalize pass. No exceptions.
- **Cache-Control on the 302 redirect** (`Cache-Control: public,
  max-age=300`) so the browser caches the S3 URL for 5 minutes and
  doesn't re-hit `/api/v1/videos/{share_id}` for repeated plays of
  the same clip.
- **Browser preload via SSE `event.video_ready`.** The
  vedanta-systems React component subscribes to the found-footy
  SSE stream and, on `event.video_ready`, sets the `<video src=...>`
  attribute IMMEDIATELY on a hidden video element (browser starts
  buffering). By the time the user clicks, the first frame is ready.
  Zero new backend infrastructure — reuses the NATS event we already
  publish.
- **Optional (Phase C follow-up)**: server-side pre-warm. When
  `event.video_ready` fires, `api` issues a small server-side GET
  against the presigned URL to warm Garage's cache before the user's
  browser asks. Deferred until we have real telemetry showing the
  browser preload isn't enough.

**Consequences:**

- Rebuild plan §7 (video pipeline) documents `-movflags +faststart` as
  a hard requirement of the `DownloadVideo` activity.
- Rebuild plan §8 SSE event catalog notes that `event.video_ready` is
  the load-bearing signal for frontend preload.
- vedanta-systems frontend gets a small change during Phase C cutover:
  the video-tile component subscribes to `event.video_ready` and
  preloads the `<video src>` when it fires.
- The play-latency bug is treated as a **known issue in Python prod
  that the Go rebuild resolves structurally**, not something to
  monkey-patch in Python. If it becomes an urgent user complaint
  before the rebuild lands, we can backport `-movflags +faststart`
  to Python `download_single_video` as a small standalone fix.

## 2026-07-01 — Workspace NATS as event bus (replaces Postgres LISTEN/NOTIFY)

**Decision:** found-footy's async event stream (SSE fan-out, webhook delivery,
cross-project events consumed by vedanta-systems) flows through workspace
NATS at [`~/workspace/nats/`](../../nats/), NOT through Postgres LISTEN/NOTIFY
as originally spec'd in earlier revisions of `docs/rebuild-plan.md` §8/§11.

**Why:** ecosystem-level decision — see workspace decisions at
`~/workspace/vedanta-dhobley/docs/decisions.md` 2026-07-01 entry for the
full rationale. NATS is designed as a broker; per-project brokers fight the
design center; cross-project consumption is trivial via a shared broker with
NATS account isolation.

**What stays vs what changes:**

- **Postgres `event_log` table stays** (per §3 schema). Still the durable
  audit trail. Every event is INSERTed into `event_log` for durability
  + backfill on SSE reconnect.
- **NATS is the fan-out mechanism.** Every event ALSO gets
  `nats.Publish(subject, payload)` for realtime delivery to subscribers.
- **SSE handler** (§8): subscribes to NATS `event.>` and `fixture.>`
  subjects on the found-footy account instead of `LISTEN`ing on a
  Postgres channel. Forwards to browser SSE consumers unchanged.
- **Webhook delivery worker** (§8): consumes NATS via a durable JetStream
  consumer with automatic replay on restart. Replaces the SQL `FOR UPDATE
  SKIP LOCKED` polling loop.

**Subject scheme (found-footy account):**

- `event.detected`, `event.stable`, `event.video_ready`,
  `event.rank_recalculated`, `event.removed`, `event.download_complete`
- `fixture.activated`, `fixture.completed`

Fully qualified via the found-footy NATS account:
`found-footy.event.video_ready` etc. — but subject strings within the
found-footy account use the short form.

**Rebuild plan impact:** §8/§9/§10/§11/§14 updated in the same commit as
this decision entry. `internal/infra/nats/` adapter added to §9;
`docker-compose.yml` no longer runs a per-project NATS in §10 (workspace
NATS is a dependency, not a project container).

---

## 2026-07-01 — Fresh rebuild in parallel, not incremental refactor

The rebuild happens as a **from-scratch build alongside the running
prod stack**, not as an in-place refactor of the existing code. Legacy
prod keeps serving traffic; new code stands up in parallel; cutover is
endpoint-by-endpoint until legacy has zero callers.

**Why**: [`design-audit.md`](./design-audit.md) surfaced structural
gaps (data-layer god-class, embedded-arrays-vs-shared-primitive, no
tests, no deploy gate, `_event_id` overloaded five ways) that are
easier to fix by building fresh than by refactoring in place. The user
built the current system while learning documentation-driven agentic
development; the rebuild is the moment to apply everything learned
since. See also [`design-audit.md`](./design-audit.md) §16
implementation order — the F-0..F-6 phases were incremental; this
rebuild subsumes them.

Codified in [`rebuild-plan.md`](./rebuild-plan.md).

---

## 2026-07-01 — Postgres over Mongo (rebuild-context reversal)

The rebuild uses Postgres for structured data. This **reverses**
[`design-audit.md`](./design-audit.md) §3's verdict ("keep Mongo"),
because the audit assumed *incremental refactor* of prod Mongo data.

**Why the framing change flipped it**: in a fresh-build context, the
backward-compat costs that made Mongo the pragmatic pick evaporate.
Postgres's native schema is stronger than Mongo's JSON Schema
validators (can't be disabled, can't run in "warn"), foreign keys
actually enforce relationships, transactions across rows are
first-class, and the extension ecosystem (`pgvector`, `pg_trgm`,
`TimescaleDB`, `pg_partman`) bolts on new capabilities without new
services. `$addToSet` idempotency becomes `INSERT ... ON CONFLICT DO
NOTHING`; embedded arrays become normalized tables with joins. In a
fresh build these are the correct shapes.

**When the audit still applies**: for the *legacy read-side compat*
layer (queries against pre-cutover `fixtures_completed` documents),
Mongo stays online read-only. New writes go to Postgres exclusively.

---

## 2026-07-01 — Garage over MinIO for blob storage

The rebuild uses Garage (Rust, ~50 MB, S3-compatible, filesystem-backed)
instead of MinIO for blob storage.

**Why**: MinIO Inc.'s "Aistor" rebrand has visibly de-featured the
community AGPL release (web console removed, replication features
behind commercial license, commit cadence slowed). Betting on
community MinIO long-term is a real risk. Garage is actively
maintained, low resource footprint, and its on-disk format is just
content-hashed files in a directory tree — no proprietary format,
`ls`-able, `rsync`-backupable. Per-project isolation (each project's
docker-compose runs its own Garage instance) preserves the workspace
pattern established by Temporal / Postgres / etc.

**Application impact**: S3-compatible API means `boto3` / `aioboto3`
client code is unchanged. Only `S3_ENDPOINT` in `.env` differs.

---

## 2026-07-01 — LLM endpoint abstracted; nexus swap is config-only

The application never knows which LLM inference server it's talking to.
All calls go through a client that reads `LLM_ENDPOINT_URL` from
environment. Model IDs are discovered from `/v1/models` at startup.
Request/response shapes conform to OpenAI's chat-completions API,
which llama.cpp (joi today) and nexus (est. end of 2026) both speak.

**Why**: nexus is under active development at `~/workspace/nexus/`
and will replace joi as found-footy's LLM endpoint when ready. The
switch must be a `.env` edit + container restart, not a code change.
This decision codifies the abstraction as a load-bearing invariant.

**Consequence for [`design-audit.md`](./design-audit.md) §6**: the
Track-1 workspace LLM gateway proposal is deferred pending nexus's
eventual API surface. If nexus provides concurrency/priority/routing
built-in, the gateway is redundant. If nexus doesn't, a thin
found-footy-side traffic shaper (not a workspace-wide gateway) may
be needed. Decide after nexus lands.

---

## 2026-06-30 — Cross-doc linking via markdown, no `[[wiki-links]]`

Docs reference each other via markdown `[text](./path.md)` syntax;
no Obsidian-style `[[]]` wiki-links anywhere in the project.

**Why**: markdown links work in every rendering context (GitHub,
terminal, IDE, Obsidian's graph view). `[[]]` is Obsidian-only —
GitHub renders it as literal non-clickable brackets, IDEs treat it as
plain text, and it's ambiguous when filenames collide across project
vaults. The "knowledge graph" effect comes from richly interconnecting
docs, not the syntax — markdown achieves the same graph in Obsidian.

Codified in global AGENTS.md § Cross-doc linking; project-side
reminders in [AGENTS.md](../AGENTS.md) § Documentation and docstrings
and [docs/README.md](./README.md) intake rules.

---

## 2026-06-30 — Brain-stack (Khoj + basic-memory MCP + Obsidian vault) deprecated

The `~/workspace/obsidian/` self-hosted second-brain stack was deleted
on 2026-06-30. Never had containers running in practice, the vault was
empty of notes, the only configured project symlink was legal-tender's
`docs/`.

**Why deprecated**: the conceptual goal (cross-project knowledge
access via interlinked markdown) is now achieved by
`~/workspace/vedanta-dhobley/` as the user-global hub plus each
project's `docs/` with rich markdown cross-linking — no separate
aggregation layer or MCP-write-to-vault tooling required.

Global AGENTS.md was cleaned of the stale Khoj / basic-memory MCP /
vault references the same day; legal-tender's six file references
queued for cleanup in `~/workspace/vedanta-dhobley/docs/todo.md`.

---

## 2026-05 — Caddy fronts all HTTP; host ports dropped

HTTP services (Temporal UI, Mongo UI, MinIO console, Twitter VNC) moved
from publishing host ports to being reached through the workspace `proxy`
Caddy via per-project hostnames. Per-project Caddyfile lives in
`~/workspace/proxy/caddy/caddy.d/found-footy.caddy`. Only host port kept
is dev Temporal gRPC `7233` for host-side dev clients.

**Why**: avoids port-allocation thrash across the ~5 personal projects on
the node, makes per-project URLs memorable
(`found-footy-prod-temporal-ui.<base-domain>` beats `localhost:3200`), and
unifies access path with how `joi` already serves `llama-small.joi`.

See: `deploy/INFRA-NOTES.md`, commits `c71dbe3` + `c9d1c77`.

---

## 2026-XX — LLM URL switched to Caddy hostname on joi

`LLAMA_URL` moved from `http://joi.<tailnet>.ts.net:3102` to
`http://llama-small.joi` (no port). joi runs its own Caddy that maps the
model-named subdomain to whatever llama.cpp port that model is currently on.

**Why**: stable URL across model swaps and port reassignments. Project
code no longer needs to know which port a given model uses.

---

## (pre-history) — Scoped deduplication by `timestamp_verified`

In `UploadWorkflow`, incoming videos are split by `timestamp_verified`
before perceptual-hash comparison: verified pool compares only against
verified S3 videos, unverified against unverified, both via parallel
`asyncio.gather()`.

**Why**: a verified goal clip and an unverified clip from a different
match moment can hash-match (same broadcast, same camera angles). Without
scoping, the verified clip could be replaced by the unverified one. In
prod this correctly blocked a Goal 1 clip from being replaced by a Goal 2
clip in the same match (expected ~31′, got 15′).

---

## (pre-history) — Workflow-ID arrays over counters

`_monitor_workflows`, `_download_workflows`, `_drop_workflows` are arrays
of workflow IDs, mutated via MongoDB `$addToSet`. The previous design
used plain integer counters incremented after work completed.

**Why**: idempotent (re-running the same workflow can't double-count),
auditable (you can see which workflows participated), failure-resistant
(a failed workflow simply doesn't register, so counts naturally stay low
without manual decrement).

---

## (pre-history) — `signal-with-start` for serialized `UploadWorkflow`

Each event has exactly one `UploadWorkflow` with deterministic ID
`upload-{event_id}`. `DownloadWorkflow`s queue videos onto it via
`signal-with-start`. The upload workflow processes signals FIFO and exits
after 5 min of idle.

**Why**: multiple parallel downloads completing simultaneously would race
on S3 dedup and produce duplicate uploads. Serializing per event
eliminates the race; deterministic ID + signal-with-start handles "is
there one already running?" without an explicit check.

---

## (pre-history) — Twitter alias resolution inside `TwitterWorkflow`

Originally there was a separate `RAGWorkflow` that resolved aliases then
fire-and-forgot into `TwitterWorkflow`. That double fire-and-forget chain
produced duplicate Twitter workflows when either side retried. Now
`TwitterWorkflow` does the cache lookup (or RAG fallback) inline at its
own startup.

**Why**: one workflow, one set of retry semantics, no orphan duplicates.
The `RAGWorkflow` is still around but only for pre-caching during
`IngestWorkflow`.

---

## (pre-history) — 5-collection MongoDB design with `fixtures_live` as overwrite buffer

`fixtures_staging` (upcoming) → `fixtures_active` (in-progress, enhanced
with our `_*` fields) → `fixtures_completed` (archive, 14-day retention).
`fixtures_live` is a temporary overwrite buffer for raw API data so we can
diff against the enhanced events in `fixtures_active` without destroying
our enhancements. `team_aliases` is a persistent RAG cache.

**Why**: the earlier single-collection-merge approach kept overwriting our
enhancement fields (`_monitor_workflows`, `_s3_videos`, etc.). The
5-collection design treats "raw API state" and "our enhanced state" as
two separate documents that we only ever compare, never merge in place.

---

## (pre-history) — Auto-scaling via dedicated scaler container

`docker compose up -d` brings up infrastructure + a single scaler service.
`worker` and `twitter` are marked `profiles: ["managed"]` so they aren't
started by default. The scaler reads Temporal queue depth + the MongoDB
active-goal count every 30 s and uses `docker compose up --scale` to
adjust replicas between 2 and 8.

**Why**: Python's GIL limits each worker to one CPU core; the workload is
spiky (zero goals one minute, eight simultaneous the next). Hand-tuning
replica count doesn't work — the scaler reacts in seconds.

---

## (pre-history) — Fire-and-forget child workflows with `ABANDON` parent close policy

`MonitorWorkflow → TwitterWorkflow` and `TwitterWorkflow → DownloadWorkflow`
both use `start_child_workflow(... parent_close_policy=ParentClosePolicy.ABANDON)`.

**Why**: parent workflows have short cycles (Monitor is every 30 s, Twitter
loops ~10 min) — child workflows must outlive them. ABANDON means parents
can complete and be GC'd while children continue independently. Without
this, children would be cancelled when parents exit and we'd lose work.

---

## (pre-history) — Heartbeat-based timeouts for long-running activities

Activities like `generate_video_hash` and `deduplicate_videos` use
`heartbeat_timeout` instead of (or in addition to) `start_to_close_timeout`.
They emit `activity.heartbeat(...)` between units of work — e.g., every 5
frames during hash generation, every video during S3 comparison.

**Why**: arbitrary execution timeouts kill long-but-progressing activities
on big videos. Heartbeats prove the activity is actually making progress —
Temporal only fails it if heartbeats stop, not if total time exceeds an
arbitrary deadline.
