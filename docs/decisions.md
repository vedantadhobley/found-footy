# Architectural Decisions

Append-only. Newest at top. Dates in `YYYY-MM-DD`. When migrating an old
decision because the rationale changed, leave the old entry in place and
add a new one above it pointing at the change.

---

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
