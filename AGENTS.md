# Found Footy — Agent Context

Real-time football goal video discovery and curation. Polls API-Football
every 30 s for live matches across the top-5 European leagues + ~15 FIFA
national teams, debounces detected goals over 3 polls, then fires off a
10-attempt Twitter video search for each one. Downloads candidate clips,
AI-validates the broadcast clock against the API's reported match minute
(Qwen3-VL-8B on `joi`), perceptually deduplicates against the existing S3
corpus, and surfaces the surviving clips through the `vedanta-systems`
portal via SSE.

This file is your front door. Read it first; follow the imports below.

## Run

```bash
docker compose -f docker-compose.dev.yml up -d         # dev
docker compose -f docker-compose.yml up -d             # prod (auto-scaler manages workers + twitter)
```

URLs (replace `<base-domain>` with the value of `$BASE_DOMAIN`):
- Temporal UI: `http://found-footy-{dev,prod}-temporal-ui.<base-domain>`
- MongoDB UI: dev `http://found-footy-dev-mongoku.<base-domain>` · prod `http://found-footy-prod-mongo-express.<base-domain>`
- MinIO console: `http://found-footy-{dev,prod}-minio.<base-domain>`
- Twitter VNC: `http://found-footy-{dev,prod}-twitter[-vnc].<base-domain>` (prod needs the `vnc` profile started)

See `deploy/INFRA-NOTES.md` for Caddy routes + cross-project network setup.

## Stack

- **Workers**: Python 3.10 (image is `python:3.10-slim`), Temporal Python SDK, asyncio. Single task queue `found-footy`; 6 workflows, 42 activities. Per-worker concurrency: 10 workflow tasks + 30 activities.
- **Workflow orchestration**: Temporal (its own Postgres for metadata, separate from MongoDB). Two scheduled workflows — `IngestWorkflow` daily 00:05 UTC, `MonitorWorkflow` every 30 s with `SKIP` overlap policy.
- **Application database**: MongoDB 7, 5-collection design — `fixtures_staging` / `fixtures_live` / `fixtures_active` / `fixtures_completed` / `team_aliases`. `fixtures_live` is an overwrite buffer so we can diff fresh API data against enhanced events in `fixtures_active` without destroying our enhancement fields.
- **Object storage**: MinIO (S3-compatible) at `minio:9000` cluster-internal.
- **Twitter scraping**: Firefox + Selenium in a dedicated `twitter/` container — browser automation, not the official Twitter API. Cookies persisted to a docker volume + `~/.config/found-footy/twitter_cookies.json` backup.
- **External LLM**: Qwen3-VL-8B (vision) via llama.cpp on `joi`, reached at `http://llama-small.joi` (Caddy split-DNS, no port). Used for video validation (is this soccer? phone-filming-a-TV? extract broadcast clock) and RAG team-alias selection.
- **Auto-scaler**: a sidecar Python service that watches Temporal queue depth + the MongoDB active-goal count and scales the `worker` and `twitter` Compose services between 2 and 8 replicas (30 s check interval, 60 s cooldown).

## Where to look first

- @README.md — public-facing project description
- @docs/architecture.md — 5-collection MongoDB design, workflow hierarchy, video pipeline, scoped dedup, schemas, activity reference
- @docs/orchestration.md — event lifecycle state machine, debouncing, VAR handling
- @docs/temporal.md — per-activity timeouts / retries / heartbeats
- @docs/logging.md — structured-JSON logging reference for Grafana/Loki (modules, actions, fields, queries)
- @docs/rag.md — Wikidata + LLM team-alias pipeline (header note flags design-stage sections)
- @docs/twitter-auth.md — browser automation, cookie lifecycle, VNC re-auth flow
- @docs/operations.md — runbook (bring-up, scaling, common issues, debugging)
- @docs/decisions.md — append-only architectural decisions log
- @docs/todo.md — active work + open bugs (paste-ready block at top)
- @docs/proposals/ — design docs for future work: `dedup-unification`, `event-matching`, `geo-restriction-bypass`, `qwen-embeddings`
- @deploy/INFRA-NOTES.md — Caddyfile entries + cross-project network setup outside this repo

## Frontend integration

Found Footy does **not** have a frontend in this repo. The unified portal at
`vedanta.systems` (sources in `~/workspace/dev/vedanta-systems/`, React +
TypeScript + shadcn/ui) hosts the UI and proxies `/api/found-footy/*` to a
Node API that reads MongoDB directly over the `luv-prod` shared docker
network. Goal videos appear live via Server-Sent Events; the worker
notifies the frontend through `monitor_activities.notify_frontend_refresh`.

Same shape as `long-exposure` and `spin-cycle`: per-project repos ship a
backend, vedanta-systems hosts the UI.

## Conventions

- **Commits**: no `Co-Authored-By` trailer. Lowercase prefix style: `feat:`, `fix:`, `chore:`, `docs:`, `perf:`, `refactor:`, `test:`. Scope in parens when useful: `fix(twitter):`, `perf(dedup):`.
- **No host ports for HTTP services.** Everything routes through the workspace `proxy` Caddy on hostname (`found-footy-{dev,prod}-*.<base-domain>`). Internal data services (mongo, postgres, minio s3-api, temporal gRPC, twitter service API on `:8888`) live on the `found-footy-{dev,prod}` bridge network only. The one exception is dev temporal gRPC published on host `7233` for host-side dev clients.
- **Tailnet identifier**: do NOT commit the FQDN to any tracked file. `.env` is gitignored; `.env.example` uses placeholders. `<base-domain>` is interpolated from `BASE_DOMAIN` at compose time.
- **Logging**: structured JSON via `src.utils.footy_logging`. Use `from src.utils.footy_logging import log` then `log.info(activity.logger, MODULE, action, msg, **fields)`. Never `print()` outside `worker.py`'s startup banner. The full module/action vocabulary lives in @docs/logging.md.
- **Code organization**: workflows in `src/workflows/`, activities in `src/activities/` (one file per domain), data adapters in `src/data/` (`mongo_store.py`, `s3_store.py`), shared utilities in `src/utils/`. The browser-automation service lives in `twitter/` as its own Python process inside its own container.

## Things to check before doing X

- **Adding a new service, hostname, or network**: read @deploy/INFRA-NOTES.md first. Caddyfile entries live in `~/workspace/proxy/caddy/caddy.d/found-footy.caddy`, NOT here. Cross-project dependency is on `vedanta-systems-{dev,prod}-api:3001` reached over `luv-{dev,prod}` — that network must exist.
- **Touching the Twitter pipeline**: Twitter workflow IDs include the API's `minute` and `extra` fields — `f"twitter-{team}-{player}-{minute_str}-{event_id}"`. If the API updates a goal's reported minute (e.g. 45 → 45+2 as stoppage extends), the workflow ID changes and a fresh Twitter workflow gets started even though the same `event_id` already has one running. **Known suspect for the double-firing bug; see @docs/todo.md before adding new workflow-ID logic.**
- **Touching `check_twitter_workflow_running`** (`src/activities/monitor.py:700-762`): the gate only treats `RUNNING` and `COMPLETED` as "don't restart". `FAILED` / `TIMED_OUT` / `TERMINATED` / `CANCELED` fall through, so every 30 s monitor cycle re-spawns Twitter if it died once. **Known bug — fix together with the workflow-ID issue above.**
- **Touching dedup logic** (`src/activities/upload.py`): the S3-comparison loop at line ~614 currently `break`s at the first matching S3 video, so multiple S3 duplicates for the same event aren't collapsed. Design in @docs/proposals/dedup-unification.md.
- **Touching the perceptual hash code** (`_generate_perceptual_hash` in `download.py`; `_perceptual_hashes_match`/`_dense_hashes_match`/`_hamming_distance` in `upload.py`): the plan is to replace hash-and-Hamming with Qwen image embeddings + cosine similarity (@docs/proposals/qwen-embeddings.md). Avoid investing in the current hash flow unless fixing a bug.
- **`UploadWorkflow` serialization**: one workflow per event with deterministic ID `upload-{event_id}`. Multiple `DownloadWorkflow`s feed it via `signal-with-start`; signals queue FIFO. Don't introduce parallel uploads per event — the scoped-dedup invariant depends on ordered processing.
- **AI vision calls** (`validate_video_is_soccer` in `download.py`): the prompt expects a structured JSON response with `SOCCER` / `SCREEN` / `CLOCK` / `ADDED` / `STOPPAGE_CLOCK` fields. The current prompt was recovered from a running prod container (commit `7d5429d`) — don't rewrite without re-validating against `scripts/test_structured_extraction.py`.
- **Adding a new event type** (Red Card etc.): flip `enabled: True` in `src/utils/event_config.py`, set `scrapeable_details` and `debounce_fields`. Confirm the search-query builder in `event_enhancement.py` handles the new type.
- **LLM calls**: only inside the Temporal activity that needs them — currently `download.validate_video_is_soccer` (vision) and `rag.get_team_aliases` (text). Endpoint is `LLAMA_URL=http://llama-small.joi`. Verify reachability: `curl $LLAMA_URL/v1/models` before adding new code. Per the host budget in `~/.claude/CLAUDE.md`, the joi LLM has a hard cap of **2 concurrent calls** — throughput drops sharply past that.
- **Operational rule (load-bearing)**: do NOT `ssh vedanta@joi` from this repo's tooling. joi is a separate node — HTTP queries over the tailnet are fine, shell commands need explicit user approval.

## Reference material

- llama.cpp on joi: live model IDs via `curl http://llama-small.joi/v1/models`
- API-Football: <https://www.api-football.com/documentation-v3> (key in `.env`)
- Temporal Python SDK: <https://docs.temporal.io/develop/python>
- Wikidata SPARQL: <https://query.wikidata.org/> (used by the RAG pipeline)

## Active state

- **Pipeline**: working end-to-end in prod. Daily ingest (00:05 UTC) → 30 s monitor → 3-poll goal debounce → Twitter discovery (10 attempts × ~1 min) → download → AI clock validation → scoped dedup → S3 upload → SSE refresh to vedanta-systems.
- **Auto-scaling**: scaler service runs continuously; both worker and twitter pools sit at 2 replicas when idle and scale up to 8 on Champions League nights.
- **Caddy migration (recent)**: HTTP services moved off host ports onto Caddy hostnames on the workspace `proxy` network. Per-project Caddyfile at `~/workspace/proxy/caddy/caddy.d/found-footy.caddy`. See `deploy/INFRA-NOTES.md`.
- **Known bugs** (full list + investigation hooks in @docs/todo.md):
  - Twitter workflow stuck "extracting" for hours on the 33′ goal in the recent Lazio v Pisa fixture — suspects above.
  - S3 dedup `break`s at first match instead of collapsing all matching duplicates.
- **Open feature work** (designs in @docs/proposals/):
  - Replace perceptual hashes with Qwen image embeddings.
  - Re-attribution recovery on goal-scorer changes (this branch, `feature/event-matching`).
  - Geo-restricted broadcaster CDN bypass.
- **Current branch**: `feature/event-matching` — designing the re-attribution recovery flow. Not yet merged to `main`.

---

> **Auto-memory note** — per `~/.claude/CLAUDE.md`, project facts belong here or in `docs/`, **not** in `~/.claude/projects/<project>/memory/`. Keep that auto-memory directory essentially empty.
