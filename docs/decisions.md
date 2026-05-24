# Architectural Decisions

Append-only. Newest at top. Dates in `YYYY-MM-DD`. When migrating an old
decision because the rationale changed, leave the old entry in place and
add a new one above it pointing at the change.

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
