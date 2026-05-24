# Sprint Plan — May 2026 Cleanup

Concrete execution plan for the audit findings. Each sprint is sized to one
focused session. Items are ordered within each sprint to minimize churn.
"Verification" is what we check before committing, not what we add as a test.

See `docs/audit.md` for the rationale behind each fix and `docs/todo.md` for
the rolling backlog state.

---

## Sprint 1 — Correctness fixes that pay back today (~3h)

**Goal**: stop the Lazio-Pisa-style runaway and the latent crashes the audit found. All edits are small; the leverage per line is enormous.

### Files touched (4)
- `src/workflows/monitor_workflow.py`
- `src/activities/monitor.py`
- `src/activities/download.py`
- `src/activities/ingest.py`
- `src/api/api_client.py`
- `src/workflows/twitter_workflow.py` (small cleanup)

### Changes

1. **Stabilize Twitter workflow ID** — `monitor_workflow.py:171-175`
   - Replace `f"twitter-{team_clean}-{player_last}-{minute_str}-{event_id}"` with `f"twitter-{event_id}"`.
   - Delete the now-unused `minute_str` line, the `player_last = player_name.split()[-1] if player_name else "Unknown"`, and the `team_clean = ...replace(...).` chain.
   - Workflow visibility is preserved via the event_id, which already encodes fixture/team/player.

2. **Expand `check_twitter_workflow_running` to all terminal states** — `monitor.py:728-762`
   - Add explicit handling: `is_terminal_success = (status == "COMPLETED")`, `is_terminal_failure = status in ("FAILED", "TIMED_OUT", "TERMINATED", "CANCELED", "CONTINUED_AS_NEW")`, `is_running = (status == "RUNNING")`.
   - Return shape: `{"exists": True, "running": is_running, "status": status_name, "is_terminal": is_terminal_success or is_terminal_failure}`.
   - On RPC error (line 749-754): return `{"exists": None, "running": False, "status": "UNKNOWN", "is_terminal": False}` — caller treats `None` as "skip restart this cycle, retry next cycle".
   - On generic exception (line 755-762): same `"UNKNOWN"` response.

3. **Update the caller to use the expanded gate** — `monitor_workflow.py:186-198`
   - Skip restart if `workflow_status["running"]` OR `workflow_status["is_terminal"]` OR `workflow_status["exists"] is None`.

4. **Set explicit `id_reuse_policy` on all `start_child_workflow` / `start_workflow` calls**
   - `monitor_workflow.py:200-221` (Twitter): `id_reuse_policy=WorkflowIDReusePolicy.REJECT_DUPLICATE` (paired with the now-stable ID and the gating activity).
   - `twitter_workflow.py:469-478` (Download): `id_reuse_policy=WorkflowIDReusePolicy.REJECT_DUPLICATE` — each attempt has its own ID via `f"download{attempt}-..."`, and we don't want duplicates.
   - `upload.py:73-85` (Upload, via `start_workflow`): replace the whole pattern with `client.signal_with_start_workflow(...)` — see Sprint 2 item 5; or as an interim fix, add `id_reuse_policy=WorkflowIDReusePolicy.ALLOW_DUPLICATE`.

5. **Fix `MODULE` NameError** — `download.py:819`
   - Add `MODULE = "download"` near the top of the file (matches the convention `upload.py:14` uses) and reference it. Or: replace the single `MODULE` use at line 819 with the string literal `"download"`. The constant approach is cleaner; the file uses `"download"` strings inline everywhere else.

6. **Fix `obj` NameError** — `ingest.py:342`
   - The except branch references `obj` which was a loop variable that doesn't exist at this scope. Either move the except inside the loop, or pass the failing key via closure. Simplest: wrap each `delete_object` call in its own try/except.

7. **Add timeout to `get_team_info`** — `api_client.py:240`
   - Add `timeout=10` to match every other call in the module.

8. **Remove the no-op `'download_count' in dir()` guard** — `twitter_workflow.py:506`
   - Initialize `download_count = 0` before the while loop (line 247-ish). Drop the guard expression at line 506.

### Verification
- Run `pytest tests/test_activity_registration.py tests/test_clock_parsing.py` — both should still pass.
- Spin up the dev stack: `docker compose -f docker-compose.dev.yml up -d --build`.
- Watch a live Twitter workflow for a real goal (or simulate one via `tests/workflows/test_pipeline.py`). Verify in Temporal UI: only ONE `twitter-{event_id}` workflow per goal even across stoppage extensions.
- Force a TwitterWorkflow failure (kill the joi LLM mid-run) and verify the monitor cycle does NOT spawn a replacement.

### Risk
- The `id_reuse_policy=REJECT_DUPLICATE` change could surface other callers that *relied* on the implicit `ALLOW_DUPLICATE_FAILED_ONLY`. Check Temporal UI for any "WorkflowAlreadyStartedError" rejections after deploy.
- If `event_id` collisions exist in the wild (they shouldn't — `{fixture}_{team}_{player}_{type}_{seq}` is unique), the workflow-ID dedup will incorrectly merge them. Run `mongo find` for duplicate `_event_id` values before deploy.

### Commit shape
One commit per logical change. Suggested: `fix(twitter): stabilize workflow ID + expand restart gate (Lazio Pisa bug)`, `fix(download): NameError on MODULE constant`, `fix(ingest): NameError in cleanup exception handler`, `fix(api): add missing timeout to get_team_info`, `chore(twitter-workflow): remove no-op download_count guard`.

---

## Sprint 2 — Mongo atomicity + signal-with-start (~3h)

**Goal**: eliminate the four race-condition classes the audit found in `mongo_store.py`, plus the signal-with-start silent-drop in upload.py.

### Files touched (3)
- `src/data/mongo_store.py`
- `src/activities/upload.py`
- `src/workflows/monitor_workflow.py`

### Changes

1. **Atomic `add_drop_workflow_and_check`** — `mongo_store.py:660-703`
   - Replace the `$addToSet` + separate `find_one` with `find_one_and_update`:
     ```python
     result = self.fixtures_active.find_one_and_update(
         {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
         {"$addToSet": {f"events.$.{EventFields.DROP_WORKFLOWS}": workflow_id}},
         projection={f"events.$": 1},
         return_document=ReturnDocument.AFTER,
     )
     ```
   - Then compute `count = len(result["events"][0][EventFields.DROP_WORKFLOWS])` and `should_delete = count >= 3`. Single atomic round-trip; no race.

2. **Atomic `check_and_mark_download_complete`** — `mongo_store.py:803-849`
   - Use a single `find_one_and_update` with an `$expr`-gated `$set`:
     ```python
     result = self.fixtures_active.find_one_and_update(
         {
             "_id": fixture_id,
             "events": {"$elemMatch": {
                 EventFields.EVENT_ID: event_id,
                 "$expr": {"$gte": [{"$size": f"$_download_workflows"}, required_count]},
                 EventFields.DOWNLOAD_COMPLETE: False,
             }},
         },
         {"$set": {f"events.$.{EventFields.DOWNLOAD_COMPLETE}": True}},
         projection={f"events.$": 1},
         return_document=ReturnDocument.AFTER,
     )
     ```
   - `result` is `None` if the conditions weren't met (count < required OR already complete). One round-trip.

3. **Transactional `activate_fixture_with_data`** — `mongo_store.py:248-253`
   - Either wrap insert+delete in a `with self.client.start_session() as session: with session.start_transaction():` block (requires replica set), OR reorder: insert to active first, THEN delete from staging only if insert succeeded.
   - The reorder is simpler and our MongoDB is single-node (no replica set). Use that.

4. **Transactional `complete_fixture`** — `mongo_store.py:1411-1441`
   - Same reorder: insert to completed first, then delete from active. If the insert fails, the fixture stays in active and the next monitor cycle retries cleanly.

5. **Replace `start_workflow + start_signal` with `signal_with_start_workflow`** — `upload.py:73-85`
   - The SDK helper handles "already running" correctly. Pattern:
     ```python
     await client.start_workflow(
         UploadWorkflow.run,
         id=workflow_id,
         task_queue="found-footy",
         start_signal="add_videos",
         start_signal_args=[batch],
         id_reuse_policy=WorkflowIDReusePolicy.ALLOW_DUPLICATE,
     )
     ```
   - If the SDK exposes a true `signal_with_start_workflow`, prefer that; otherwise the `start_workflow + start_signal + id_reuse_policy=ALLOW_DUPLICATE` form is equivalent.

6. **`asyncio.gather(..., return_exceptions=True)` in monitor cycle** — `monitor_workflow.py:254`
   - Wrap each fixture task in its own try/except that returns a structured error dict. Then `asyncio.gather(*tasks, return_exceptions=True)`, filter exceptions, log them with structured `log.error(..., fixture_id=fid, error=str(exc))`. One fixture failure no longer poisons the cycle.

7. **Fix MD5-dedup silent miss** — `upload.py:352`
   - Change `s3_video.get("s3_key", "")` to `s3_video.get("_s3_key", "")`. Keep the `or s3_url.replace(...)` fallback in case some old MongoDB rows have only `url`.

8. **Fix malformed `$pull` in `update_video_in_place`** — `upload.py:945`
   - The positional `$` inside `$pull` doesn't do what it looks like. Use `arrayFilters`:
     ```python
     {"$pull": {"events.$[evt]._s3_videos": {"url": old_s3_url}}}
     # with array_filters=[{"evt._event_id": event_id}]
     ```
   - Verify with a test against `update_video_in_place`, `replace_s3_video`, `bump_video_popularity` — all three use similar patterns.

9. **Fix `_create_indexes` partial-failure** — `mongo_store.py:75-108`
   - Move `_indexes_created = True` into a `finally:` block. Better: split the 12 index creations into individual try/except (with action="index_create_failed" log) so one bad index doesn't skip the others.

### Verification
- Run the existing test suite (`pytest tests/test_activity_registration.py tests/test_clock_parsing.py`).
- For each atomicity fix, write a one-shot verification:
  - Spawn two MonitorWorkflows simultaneously on the same fixture; verify `_drop_workflows` doesn't double-delete.
  - Spawn two DownloadWorkflows that both finish registering at the same time, then verify `_download_complete` flips exactly once.
- For the signal-with-start fix: start UploadWorkflow, let it idle-timeout, then send a fresh signal — verify the batch lands and is processed.
- For the `$pull` fix: insert a fixture with multiple S3 videos, run `update_video_in_place` on one, verify only that one is removed.

### Risk
- The Mongo atomicity changes are subtle. If any of the `find_one_and_update` queries have a typo, a workflow may stall waiting for a condition that can never be true. Test by hand on dev before deploying to prod.
- The `$pull` arrayFilters change requires MongoDB ≥ 3.6 (we're on 7, so fine).
- Reordering insert/delete in `complete_fixture` and `activate_fixture_with_data` means a crash *now* leaves the source document intact rather than losing it — strictly better, but the failure modes downstream change shape.

### Commit shape
One commit per fix: `fix(mongo): make add_drop_workflow_and_check atomic`, etc.

---

## Sprint 3 — Dead code purge + dep pinning (~2h)

**Goal**: delete ~900 lines of confirmed dead code, pin requirements.txt, remove unused dependencies. Mechanical, satisfying, makes everything else easier to read.

### Files touched (many — mostly deletions)

### Changes

1. **Delete whole files**
   - `rm src/workflows/rag_workflow.py` (217 lines, would TypeError if invoked)
   - `rm twitter/auth.py` (298 lines, dangerous automated-login path)
   - `rm twitter/manual_login.py` (overlaps `firefox_manual_setup.py`)
   - Remove their imports from `src/workflows/__init__.py`, `src/worker.py` (RAGWorkflow registration), `twitter/__init__.py`.

2. **Delete registered-but-unused activities**
   - Remove from `src/worker.py` activity registration list:
     - `monitor.fetch_staging_fixtures`
     - `monitor.sync_fixture_metadata`
     - `monitor.register_monitor_workflow`
     - `upload.replace_s3_video`
   - Delete the function definitions in their respective files.

3. **Delete module-level dead globals**
   - `download.py:21, 26-27`: `_download_lock`, `_last_download_time`, and the comment.

4. **Delete dead functions** (full list in `docs/audit.md` §3)
   - `models.py`: `FixtureStatus`, `EventType`, `VideoFields`, `TeamType` enums + all raw API TypedDicts (lines 67-484) + `create_event_id()` helper. Replace with a 2-line note pointing at `src/utils/fixture_status.py` and `src/utils/event_config.py`.
   - `mongo_store.py`: `fixtures_insert_staging`, `delete_team_alias`, `clear_team_aliases`, `clear_top_flight_cache`.
   - `s3_store.py`: `upload_video_file`, `list_goal_videos`, `get_existing_video_hashes`, `get_bucket_stats`, `tag_fixture_directory`, `get_video_tags`.
   - `scaler/scaler_service.py`: lines 313-323 (`scale_up`/`scale_down`), 393-407 (`scale_twitter`/`scale_workers`), 516-552 (`get_healthy_twitter_urls`).
   - `scaler/registry.py:182-203`: `mark_instance_busy`, `mark_instance_available`.
   - `utils/team_data.py:160-166, 174-176`: `get_uefa_team_ids` (duplicate), `get_legacy_uefa_ids`.
   - `api_client.py:271-331`: `test_events_api_debug`.

5. **Delete unreachable code branches**
   - `download.py:899-935`: text-fallback parser in `parse_response` (unreachable with `response_format={"type":"json_object"}`).
   - `download.py:85-219`: `parse_broadcast_clock` (only used by tests/scripts; production uses `parse_clock_field`). Verify no other call sites first.
   - `upload.py:1153-1170, 1287-1334, 1429-1467`: `_hamming_distance`, legacy-format branch in `_perceptual_hashes_match`, `_parse_perceptual_hash`. All for a hash format with no live producer.

6. **Delete duplicate imports**
   - `download.py:548, 1487` (duplicate `import re`, `import json`).
   - `twitter.py:19` (duplicate `import os`).
   - `worker.py:48` (duplicate `from datetime import timedelta`).

7. **Fix or move broken tests**
   - `tests/test_rag_pipeline.py`: fix the imports (`get_uefa_teams` → `get_top_flight_team_ids`, `get_fifa_teams` → `get_fifa_team_ids`) and replace Ollama references with `LLAMA_URL`. OR move to `scripts/` so it stops pretending to be a test. Simpler: move to `scripts/test_rag_pipeline.py`.
   - `tests/test_simple_wikidata.py`: also exploratory; move to `scripts/` if keeping, otherwise delete.

8. **Pin requirements.txt + remove unused deps**
   - Inside a worker container: `pip freeze | grep -v "^-e" > /tmp/req.txt`. Diff against current `requirements.txt`. Drop the 4 confirmed unused (`undetected-chromedriver`, `mutagen`, `psutil`, `pyOpenSSL`). Add `httpx`.
   - Replace `requirements.txt` with the pinned list. Document in a top-of-file comment that this is a manually-pinned snapshot; future work could move to `uv.lock` properly.
   - Rebuild the dev container to confirm it still starts.

### Verification
- `docker compose -f docker-compose.dev.yml build worker twitter` — must succeed.
- `pytest tests/test_activity_registration.py tests/test_clock_parsing.py tests/test_vision_validation.py` — must pass.
- Spin up dev stack and check that worker registers without errors (`docker compose logs worker | head -50`).
- `grep -rn "RAGWorkflow\|fetch_staging_fixtures\|TwitterAuthenticator\|replace_s3_video" src/ twitter/` — should return zero hits.

### Risk
- Removing the legacy hash format support assumes no S3 video in MongoDB still has a `hash:hash:hash` format. Run `db.fixtures_active.find({"events._s3_videos.perceptual_hash": {"$not": /^dense:/}})` and `db.fixtures_completed.find(...)` first. If anything matches, write a one-shot migration before deleting.
- The `models.py` deletions are large — make sure no `from src.data.models import ...` statement anywhere imports the deleted symbols. Grep before each delete.
- The dependency pinning will break if the current container has versions different from what the next `docker compose build` would have pulled. Build inside-the-container freeze is the right reference point.

### Commit shape
Suggest one commit per cluster: `chore: delete RAGWorkflow + 3 unused activities`, `chore: delete twitter/auth.py + manual_login.py`, `chore: prune models.py / mongo_store.py / s3_store.py dead helpers`, `chore: remove unreachable code branches in download.py / upload.py`, `chore(deps): pin requirements.txt + drop 4 unused packages`.

---

## Sprint 4 — Connection singletons + constants (~3h)

**Goal**: stop instantiating new MongoDB and S3 clients per activity call. Centralize the dozen-plus magic numbers the audit catalogued.

### Files touched (~15)
- `src/data/mongo_store.py`
- `src/data/s3_store.py`
- `src/utils/orchestration_config.py` (new)
- All activity files (mechanical replacement of `FootyMongoStore()` calls)

### Changes

1. **Module-level `FootyMongoStore` singleton** — `mongo_store.py`
   - Add at module top: `_store: Optional[FootyMongoStore] = None`.
   - Add factory: `def get_store() -> FootyMongoStore: global _store; if _store is None: _store = FootyMongoStore(); return _store`.
   - Update all 29 call sites: `from src.data.mongo_store import get_store` instead of `FootyMongoStore`. Replace `store = FootyMongoStore()` with `store = get_store()`.
   - **Important**: PyMongo is fork-safe via `MaxConnecting` but the singleton pattern needs to handle the Temporal worker's process model — confirm by reading the temporalio Worker's child-process behavior. In practice, worker.py creates one process per container; one singleton per worker process is correct.

2. **Module-level `FootyS3Store` singleton** — `s3_store.py`
   - Same pattern: `get_s3_store()` factory.
   - The `head_bucket` check at line 58 should NOT run on every instantiation. Move it to a `_verify_bucket_once()` method called from `get_s3_store()` only on first creation.

3. **Create `src/utils/orchestration_config.py`** (new file)
   - Collect all orchestration tunables in one named module:
     ```python
     # Twitter discovery
     TWITTER_MAX_ATTEMPTS = 15
     TWITTER_REQUIRED_DOWNLOADS = 10
     TWITTER_MAX_VIDEOS_PER_ATTEMPT = 5
     TWITTER_SEARCH_MAX_AGE_MINUTES = 3
     TWITTER_ATTEMPT_SPACING_SECONDS = 60
     TWITTER_ATTEMPT_MIN_WAIT_SECONDS = 10

     # Monitor / debounce
     MONITOR_DEBOUNCE_STABLE_COUNT = 3
     MONITOR_DROP_THRESHOLD = 3
     MONITOR_STAGING_LOOKAHEAD_MINUTES = 30
     MONITOR_STAGING_POLL_INTERVAL_MINUTES = 15

     # Upload
     UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES = 5
     UPLOAD_TASK_TIMEOUT_SECONDS = 60

     # LLM (joi cap)
     LLM_MAX_CONCURRENT_CALLS = 2

     # Standard timeouts
     ACTIVITY_TIMEOUT_FAST = timedelta(seconds=10)
     ACTIVITY_TIMEOUT_STANDARD = timedelta(seconds=30)
     ACTIVITY_TIMEOUT_SLOW = timedelta(seconds=60)
     ACTIVITY_TIMEOUT_VERY_SLOW = timedelta(seconds=90)
     ```

4. **Replace inline magic numbers** with imports from `orchestration_config`
   - `twitter_workflow.py`: `MAX_ATTEMPTS`, `REQUIRED_DOWNLOADS`, `MAX_VIDEOS_TO_DOWNLOAD`, search max_age, spacing.
   - `monitor.py`: `DROP_THRESHOLD` (also in `mongo_store.py:678`), `lookahead_minutes`, staging interval math.
   - `upload_workflow.py`: `idle_timeout`.
   - `upload.py`: `task_timeout=60` on the signal-with-start.
   - `download.py:25` + `rag.py:44`: replace `Semaphore(2)` with `Semaphore(LLM_MAX_CONCURRENT_CALLS)`.

5. **Create `src/utils/dedup_config.py`** (new file)
   - Move dedup-specific tunables:
     - `MAX_HAMMING_DISTANCE`, `MIN_CONSECUTIVE_MATCHES`, `DURATION_SIMILARITY_THRESHOLD`, hash sampling interval.
     - Vision: `VISION_SAMPLE_FRACTIONS`, `CLOCK_MATCH_TOLERANCE_MINUTES`, `FFMPEG_TIMEOUT_SECONDS`, `DEFAULT_VIDEO_DURATION_FALLBACK`, `MIN_VISION_DURATION_SECONDS`.

6. **Resolve the LLM concurrency contradiction**
   - AGENTS.md says hard cap is 2. `download.py:25` and `rag.py:44` each set `Semaphore(2)` per process. With 8 worker replicas, that's 16+ concurrent.
   - Decision needed: is the cap PER WORKER (current behavior, ok with replicas) or GLOBAL (current implementation broken)? Document the decision in AGENTS.md and either:
     - (a) Update AGENTS.md to say "2 per worker, up to N total" and live with the joi load.
     - (b) Implement a global-across-workers cap via Temporal's `max_concurrent_activity_executions` per activity type (set `task_queue` partitioning or use Temporal's per-namespace limits).
   - I lean (a) — the actual joi capacity per the global CLAUDE.md is what matters; verify by load-testing.

7. **Auto-derive worker startup banner activity count** — `worker.py:246`
   - Replace `"Activities: 42 total (4 ingest, 10 monitor, 3 rag, 6 twitter, 7 download, 12 upload)"` with `f"Activities: {len(worker.activities)} total registered"`.

### Verification
- `pytest tests/` — should all pass (no functional change).
- Dev stack starts up cleanly; monitor cycle still fires.
- Profile MongoDB connection count: before vs after Sprint 4. Should drop from ~30 to ~4 (one per worker replica).

### Risk
- The singleton pattern in `FootyMongoStore` needs to be thread-safe within a single process. Temporal's activities can run concurrently in the same process (we set `max_concurrent_activities=30`). PyMongo's `MongoClient` is thread-safe, so the singleton is OK. Just make sure the factory's `if _store is None:` initialization is itself safe — use `threading.Lock()` if needed.
- Changing imports in 29 places is mechanical but error-prone. Do it in one commit so reviewing the diff catches misses.

### Commit shape
- `perf(data): module-level FootyMongoStore + FootyS3Store singletons` (the 29-callsite change)
- `refactor(config): centralize orchestration constants into orchestration_config.py`
- `refactor(config): centralize dedup constants into dedup_config.py`
- `chore(worker): auto-derive activity count in startup banner`

---

## Sprint 5 — Module splits + counter retirement (multi-session)

**Goal**: split the three 1500+-line files into focused modules. Retire the
deprecated `MONITOR_COUNT` / `TWITTER_COUNT` writes that the audit found are
still happening.

This sprint is multi-session (5-8 hours total). Do each split as its own
commit so they're individually revertable.

### Subtask 5a — Split `download.py`
- Target structure:
  - `src/activities/download.py` (~600 lines): `register_download_workflow`, `check_and_mark_download_complete`, `download_single_video`, `_load_twitter_cookies`, `_process_downloaded_video`, `_calculate_md5`, `_get_video_metadata`, `cleanup_download_temp`.
  - `src/activities/vision.py` (~500 lines): `validate_video_is_soccer`, `parse_clock_field`, `parse_added_field`, `parse_stoppage_clock_field`, `compute_absolute_minute`, `validate_timestamp`, `_extract_frame_for_vision`, `_call_vision_model`, `_LLM_SEMAPHORE`.
  - `src/activities/hashing.py` (~200 lines): `generate_video_hash`, `_generate_perceptual_hash`.
- Update `worker.py` activity registrations.
- Update `docs/temporal.md` and `docs/architecture.md` activity tables.
- Doing the split *before* the qwen-embeddings work means `hashing.py` is a single-file delete when embeddings land.

### Subtask 5b — Split `upload.py`
- Extract `_perceptual_match_helpers` (`_hamming_distance` (if still alive after Sprint 3), `_perceptual_hashes_match`, `_dense_hashes_match`, `_parse_perceptual_hash`, `_pick_best_video_from_cluster`, `_should_replace_s3_video`, `DURATION_SIMILARITY_THRESHOLD`) into `src/utils/dedup.py`.
- Keep `upload.py` focused on the upload + replacement flow.
- Same as above: isolating the dedup helpers makes the qwen-embeddings swap surgical.

### Subtask 5c — Split `mongo_store.py`
- Target structure:
  - `src/data/store.py` — base class with connection + index management.
  - `src/data/fixtures.py` — fixtures_staging/live/active/completed CRUD.
  - `src/data/events.py` — event workflow-array tracking, monitor/download complete marking.
  - `src/data/videos.py` — video add/rank/popularity.
  - `src/data/aliases.py` — team_aliases CRUD.
  - `src/data/cache.py` — top_flight_cache (and document it as the 6th collection in architecture.md).
- Each ~200-300 lines.
- All inherit from `store.py` (or expose `get_*_store()` factories that share the singleton client).

### Subtask 5d — Retire `MONITOR_COUNT` / `TWITTER_COUNT` writes
- Audit `monitor.py:521-522, 583, 609, 616` — these are the live writes via `update_event_stable_count`.
- Audit `mongo_store.py:1400` and the completion-check at `:1370-1409` — the live reads.
- Replace counter reads with `len(_monitor_workflows)` or equivalent array length checks.
- Delete `update_event_stable_count` method.
- Delete the `MONITOR_COUNT` / `TWITTER_COUNT` constants from `models.py` (already DEPRECATED).
- Knock-on: any docs that mention the counters need updates.

### Verification
- After each split: `pytest tests/test_activity_registration.py` must pass (it inspects worker.py activity registration vs activity files).
- After 5c: dev stack must start cleanly with the new module layout.
- After 5d: a goal must still trigger Twitter at the right debounce point (3 polls). Verify in dev with a synthetic fixture.

### Risk
- Module splits change imports everywhere. Do each split as its own commit + PR so reverts are easy.
- The counter retirement could expose a hidden assumption somewhere in the frontend (`vedanta-systems`) that reads `_monitor_count`. Grep the frontend repo first.

### Commit shape
- `refactor(download): split into download/vision/hashing modules`
- `refactor(upload): extract dedup helpers into utils/dedup.py`
- `refactor(data): split mongo_store into per-domain modules`
- `chore(monitor): retire deprecated MONITOR_COUNT writes`

---

## Sprint 6+ — Feature / infrastructure work (deferred)

Feature and infra work, with proposals already written. Note that the
LLM stack redesign has tracks that are **unblocked today** — they could
conceivably interleave with the cleanup sprints (e.g., do Track 1
between Sprints 4 and 5) rather than waiting until after Sprint 5.

1. **LLM stack redesign** — `docs/proposals/llm-stack-redesign.md`. Three independent tracks:
   - Track 1 (LLM gateway): unblocked, ~1 day
   - Track 2 (chat-call reduction + RAG to llama-large): unblocked, small
   - Track 3 (image embeddings): blocked on llama.cpp support for Qwen3-VL-Embedding-8B on AMD/Vulkan
2. **Re-attribution recovery** — `docs/proposals/event-matching.md`. Design exists from the recently-merged `feature/event-matching` branch.
3. **Geo-restricted CDN bypass** — `docs/proposals/geo-restriction-bypass.md`. Not started.
4. **Dedup unification** — `docs/proposals/dedup-unification.md`. Tied to Track 3 of the LLM redesign (gets folded in when embeddings land).

---

## Out of scope

- **No new tests** are part of these sprints. The audit flagged that real test coverage is essentially nil (only `test_clock_parsing.py` and `test_activity_registration.py` are real). Adding meaningful tests is its own multi-session effort and should follow Sprint 5 once the module shape is stable.
- **No frontend changes** here. `vedanta-systems` is a separate repo with its own audit needs. Worth a side conversation about whether the "extracting" UI symptom should be re-examined once the workflow ID stabilization in Sprint 1 lands.
