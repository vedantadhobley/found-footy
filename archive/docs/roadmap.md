# Rewrite Roadmap — 2026-Q2

Committed direction (locked 2026-05-26). Scope: full 7-phase rewrite, ~12-15 days of focused work, ending with an end-to-end architecturally clean codebase. Subsumes the Sprint 1-5 plan in `docs/sprints.md` (Sprint 1 already shipped; remaining sprints are absorbed into Phases 0 + 3).

## Strategic decisions (locked)

1. **Full scope**. All seven phases below. Goal is "perfect" — error taxonomy, module structure, test coverage, embedding-based dedup, observable system, instrumented per-match coverage SLO, real frontend API boundary.
2. **Embedding migration (Phase 5)** — **deferred decision** until Phase 4 ships. Default path if we pursue it: sentence-transformers as a sibling service on joi (safetensors already cached from the 2026-05-24 test). Wait-for-Qwen is the alternative but timeline is indefinite.
3. **Frontend integration (Phase 6)** — committed to a **real HTTP API in found-footy** (FastAPI). vedanta-systems calls `/api/v1/{fixtures,events,videos}` instead of reading MongoDB directly. Right shape long-term; one-time migration cost.

## Architectural principles to preserve (do not break in the rewrite)

The three load-bearing ideas the original author got right:
1. **Idempotent workflow-ID-keyed coordination** via `$addToSet` on arrays; derive boolean flags from array lengths reaching thresholds. Failure-resistant.
2. **Bidirectional 3-poll debounce** — survives API jitter both directions.
3. **Per-event serialization for S3, parallel everywhere else** — correct invariant for dedup races.

## Phase 0 — Finish what's in motion (~1 day) ✅ SHIPPED 2026-05-26

Absorbs remaining Sprints 2-4 from `docs/sprints.md`. Closes audit §1 correctness items and §3 dead code.

- ✅ **Sprint 2**: Mongo atomicity. Replaced 4 non-atomic read-then-write patterns with `findOneAndUpdate`+`$expr` (`add_drop_workflow_and_check`, `check_and_mark_download_complete`, `activate_fixture_with_data`, `complete_fixture`). Replaced `start_workflow + start_signal` with `signal_with_start_workflow`. Added `return_exceptions=True` to MonitorWorkflow's gather.
- ✅ **Sprint 3**: Dead code purge (~900 lines deleted: `rag_workflow.py`, `twitter/auth.py`, `twitter/manual_login.py`, 4 unused activities, dead enums in models.py) + pinned `requirements.txt` + dropped 4 unused deps + added `httpx` explicitly.
- ✅ **Sprint 4 (partial)**: MongoStore singleton (29 callsites), S3Store singleton (4 callsites), created `src/utils/orchestration_config.py` for magic numbers, auto-derived activity count in worker banner.

**Exit criteria met**: audit §1 closed, ~900 LOC deleted, no `Client.connect`/`MongoClient`/`FootyS3Store` instantiated per activity call, magic numbers live in `orchestration_config.py`. All 90 tests passing. Deployed to dev, soaking.

## Phase 1 — Error taxonomy + observability (~1 day)

Foundation for everything else. We currently can't answer "is the system working?" without 30 minutes of forensics.

- **Typed errors** in a new `src/utils/errors.py`: `TwitterRateLimitedError`, `VideoGeoRestrictedError`, `VideoNotAvailableError`, `VideoDeletedError`, `LLMUnavailableError`, `LLMValidationError`, `MongoTransientError`, `MongoConflictError`, etc. Each carries context for retry decisions.
- **Replace generic `except Exception`** at the activity-boundary layer: catch typed errors first, re-raise unknown exceptions. Audit found 47 such patterns in `mongo_store.py` alone.
- **Per-event telemetry** as a first-class `_telemetry` field on the event document:
  ```
  _telemetry: {
    search_attempts: 10,
    videos_discovered: 4,
    videos_downloaded: 0,
    download_failure_reasons: {"403_geo": 2, "404_deleted": 1, "ytdlp_parse": 1},
    validation_pass_rate: 0.0,
    final_s3_count: 0,
    primary_failure_class: "download_403",
  }
  ```
- **Per-match completion summary log** at fixture move active → completed: one structured line with N goals / M videos captured / coverage rate / primary failure classes.
- **Grafana dashboards**: video-capture-rate-per-league-per-week, failure-class breakdown, p50/p95 time-from-goal-to-S3.

**Exit criteria**: a future "stuck event" or "missing videos" investigation can be answered with one Grafana query, not 30 minutes of log archaeology.

## Phase 2 — Fix status-ID truncation + download error swallowing (~half day)

Direct user-visible win — should recover some fraction of missed videos immediately.

- Log `e.__cause__` (the actual underlying exception, not the Temporal `"Activity task failed"` wrapper) in `download_workflow.py:212-221`. ~5 lines.
- Drill into the status-ID truncation pattern (3 of 4 failed Curda-goal URLs had truncated snowflake IDs). Likely culprits: yt-dlp parsing, Twitter syndication API redirect, deleted/quoted-tweet rendering. Determine the actual source and act.
- Add URL format validation BEFORE invoking the download activity — reject malformed IDs with a typed `VideoNotAvailableError` so we can count + track them.

## Phase 3 — Module reorganization (~2 days)

The "god files" problem. Concrete splits:

- `src/activities/download.py` (1672) → `src/activities/download/` package:
  - `download/core.py` (~600 LOC): download_single_video, register_download_workflow, cleanup_download_temp
  - `src/activities/vision.py` (~500 LOC): validate_video_is_soccer, clock parsers, LLM client
  - `src/activities/hashing.py` (~200 LOC): generate_video_hash, _generate_perceptual_hash — earmarked for deletion under Phase 5
- `src/activities/upload.py` (1467) → `src/activities/upload/` package:
  - `upload/core.py` (~500 LOC): upload_single_video, queue_videos_for_upload, fetch_event_data
  - `upload/dedup.py` (~400 LOC): MD5 + perceptual dedup
  - `upload/replacement.py` (~300 LOC): update_video_in_place, bump_video_popularity, replace_s3_video (still dead? confirm deletion in Phase 0)
  - `src/utils/dedup_match.py` (~250 LOC): _perceptual_hashes_match, _dense_hashes_match, _hamming_distance — also earmarked for deletion under Phase 5
- `src/data/mongo_store.py` (1608) → `src/data/` package:
  - `data/store.py` (~150 LOC): MongoStore base with client, indexes
  - `data/fixtures.py` (~300 LOC): staging/live/active/completed CRUD
  - `data/events.py` (~400 LOC): event lifecycle, workflow-ID tracking, completion marking
  - `data/videos.py` (~250 LOC): video add/rank/popularity bump
  - `data/aliases.py` (~150 LOC): RAG cache CRUD
  - `data/cache.py` (~100 LOC): top_flight_cache (now formally a 6th collection)
- `twitter/session.py` (1100+) → `twitter/` package: `session.py` (core) + `scrape.py` (DOM extraction) + `auth.py` (existing, scope unclear after Phase 0 deletion)

Each module gets a real docstring + 2-3 unit tests for its public API (Phase 7).

**Exit criteria**: largest module ≤ 600 LOC; "find the function that does X" is a one-step grep, not detective work.

## Phase 4 — Discovery hardening (~1 day)

- **Adaptive Twitter loop**: ramp down attempts when no new videos in N consecutive tries; extend the window for high-importance fixtures (Champions League, top-flight derbies). Currently rigid 10×1min regardless of fixture importance or success rate.
- **DOM-selector canary**: small hourly job that runs a known query against X and checks the result shape. Alerts when X redesigns break our scraper. Today we'd only notice when goals start disappearing.
- **Per-match coverage SLO**: alert if a tracked-league fixture finishes with < N% goal-video capture rate (N TBD; pick after Phase 1 data lands).

> Geo-restriction proxy pool **deferred indefinitely** (locked 2026-05-26) — cost concern, and current prod's video capture is already "decent". See "Out of scope" below.

## Phase 5 — Embedding migration (~1-2 days, DECISION GATE after Phase 4)

Default path: sentence-transformers as a sibling service on joi. Replaces ~400 LOC of perceptual hash code with cosine similarity. Replaces 2 of 5 chat-LLM questions (SOCCER, SCREEN) with embedding similarity to a reference set. Per-video chat calls drop from 2-3 → 1 (OCR only).

Hardware/serving choice deferred until Phase 4 ships:
- Sentence-transformers on joi (safetensors cached): default if embedding is in scope
- Qwen republishing with `1_Pooling`: unblocks the llama.cpp path (cheaper but indefinite timeline)
- Skip entirely if Phase 4 hardening + better Twitter coverage makes dedup quality non-critical

## Phase 6 — Schema cleanup + real frontend API boundary (~1-2 days)

- **New top-level `api/` package** in this repo (peer to `src/`, `twitter/`, `scaler/`, not nested under `src/`): FastAPI service serving `/api/v1/{fixtures,events,videos}` plus SSE for live goal notifications. Replaces the Express router currently in `vedanta-systems/src/server/routes/found-footy.ts` (~837 lines). SSE keeps current semantics via `sse-starlette` (FastAPI equivalent of the Express implementation).
- **Schema cleanup during port**: retire `_monitor_count`, `_twitter_count`, `_video_count`, `_download_stats` permanently. Drop legacy hash format. Clean payload shape at the API boundary so the frontend doesn't have to know about internal `_` enrichment fields.
- **URL stability is non-negotiable**: shared event/video links already in the wild MUST continue to resolve. Document the URL ↔ payload mapping in `docs/api-contract.md`; the new contract is the authoritative source.
- **Cutover**: vedanta-systems switches from direct MongoDB reads to API calls, one endpoint at a time. Old MongoDB-direct paths stay during the transition until every UI surface migrates.
- **og-server** (vedanta-systems' OpenGraph meta-tag server for shared links): keeps generating OG cards; just calls the new found-footy API instead of reading MongoDB.
- **Side-quest**: investigate the production video-playback bug ("clicking a video sometimes doesn't play it right away") — may be related to the current double-read race or SSE/cache layering. Worth confirming once the new contract is in place.

## Phase 7 — Real test coverage (~3-5 days, runs alongside other phases)

- **Unit tests** for the (now-split) data layer modules — fast, no DB (use mongomock or similar)
- **Unit tests** for vision/clock parsing — clock_parsing.py is the template; expand to vision validation
- **Unit tests** for dedup logic (the matching algorithm in isolation)
- **One integration test**: synthetic fixture lifecycle end-to-end against the dev stack
- Target: from ~5% to ~50% coverage. Perfect coverage is out of scope.

## Suggested ordering (next 2 weeks, World Cup ~3 weeks out)

```
✅ Day 1-3  Phase 0 (Sprints 2-4)                                   SHIPPED 2026-05-26
   Day 4-5  Phase 1 (error taxonomy + telemetry + Grafana)
   Day 5    Phase 2 (status-ID truncation + e.__cause__ logging)
                    ← VISIBLE WIN landed here
   Day 6-7  Phase 3 (module splits, 4 mega-files → 25+ focused modules)
   Day 8    Phase 4 (adaptive discovery + canary + SLO — no geo-proxy)
   Day 8    DECISION GATE on Phase 5
   Day 9-10 Phase 5 (embeddings, IF in scope)
   Day 10-12 Phase 6 (FastAPI + vedanta-systems migration)
   Day 12-15 Phase 7 (test coverage, ongoing in parallel)
```

Phase 7 should run ALONGSIDE phases 3-6 — every split module gets tests as it's created. The standalone "Phase 7 days" are for backfilling tests on pre-existing code.

**World Cup framing**: locked deadline ~2-3 weeks out (mid-June). Current prod ships "decent" video coverage today; this rewrite is polish + bulletproofing, not rescue. If we slip Phase 5/6/7 past WC kickoff that's acceptable — Phases 1-4 are the must-haves so we can debug WC traffic, not just observe it.

## Out of scope for this rewrite

- **Geo-restriction proxy pool** (`docs/proposals/geo-restriction-bypass.md`) — deferred indefinitely 2026-05-26. Cost vs. marginal coverage gain doesn't pencil; current prod's video capture is already "decent" without it. Revisit if a specific high-value broadcaster (e.g. World Cup feed) is consistently blocking us.
- Twitter API switch from browser scraping to the official Twitter API v2 (cost; rate limits; can revisit if scraping becomes unsustainable)
- Replacing Temporal — it's the right tool, stays
- Replacing MongoDB — also right tool, stays
- Multi-region deployment — single-node tailnet is fine for personal-project scale

## Status

- **Decided**: 2026-05-26
- **Phase 0**: ✅ shipped 2026-05-26 (4 commits to `main`, deployed to dev, 90 tests passing, soaking)
- **Sprint 1 production validation**: pending — Saint Etienne v Nice 2026-05-26 ended 0-0 so no goals to exercise the fix; Crystal Palace v Rayo Vallecano 2026-05-27 is the next opportunity
- **Phase 1 entry**: in progress (2026-05-26 evening)

See `docs/sprints.md` for the operational sprint board (per-session task lists).
