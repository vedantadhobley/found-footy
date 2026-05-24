# Code Audit — May 2026

**Snapshot.** Point-in-time inventory of rot, latent bugs, dead code, and
refactor opportunities, taken when the project was reopened after ~3-4 months
in production. All findings cite `file:line`. Ordered by actionability —
critical correctness issues first, then sweep-deletion candidates, then
structural refactors.

See `docs/todo.md` for the rolling work backlog. This file is the source
material that backlog items in `docs/todo.md` cite back to.

---

## 1. 🔥 Critical correctness bugs (fix this week)

### 1a. Lazio v Pisa Twitter runaway — root cause confirmed

The bug noted in `docs/todo.md` decomposes into three orthogonal failures.
All three are real and code-visible.

**(a) Workflow-ID built from mutable API fields.** `src/workflows/monitor_workflow.py:171-175` builds:
```
minute_str = f"{minute}+{extra}min" if extra else f"{minute}min"
twitter_workflow_id = f"twitter-{team_clean}-{player_last}-{minute_str}-{event_id}"
```
`minute`, `extra`, `team_name`, and `player_last` all come fresh from the
API on every monitor cycle. Any of them mutating (stoppage extension,
team name reformat, mid-game player name correction) changes the ID;
`check_twitter_workflow_running` lookups the new ID, finds nothing, spawns
a fresh Twitter workflow. The original keeps running.

**(b) `check_twitter_workflow_running` blocks restart only on RUNNING / COMPLETED.** `src/activities/monitor.py:728-738` returns `running = (desc.status == RUNNING)`; the caller branches on `running == True` (skip) and `status == "COMPLETED"` (skip). `FAILED / TIMED_OUT / TERMINATED / CANCELED / CONTINUED_AS_NEW` all fall through. Worse, lines `749-754` and `755-762` return `exists=False` on any RPC error or generic exception — a transient Temporal hiccup is indistinguishable from "no workflow with this ID exists" → spurious restart. No `id_reuse_policy` set on `start_child_workflow` (line 200-221) means defaults apply (`ALLOW_DUPLICATE_FAILED_ONLY`), which *permits* restart after FAILED.

**(c) `_download_workflows` array can grow past 10 from suspect (a)/(b) cascades.** `DownloadWorkflow` registers itself before any work (download_workflow.py:99-127). Each Twitter respawn from (a)/(b) starts its own 10-attempt cycle. The shared `_download_workflows` array grows past 10; `_download_complete` does eventually flip true via `mongo_store.py:803-849`, so the *completion* path works — but the **frontend likely shows "extracting" while any TwitterWorkflow is RUNNING**. If a respawn TwitterWorkflow keeps re-firing every 30 s (suspect b), the UI shows "extracting" until the fixture closes.

**Fix order:**
1. Stabilize workflow ID to `f"twitter-{event_id}"` (`monitor_workflow.py:171-175`). Eliminates (a) entirely.
2. Expand the "don't restart" set in `check_twitter_workflow_running` to all terminal states; return a third state `"unknown"` on RPC/generic errors and have the caller skip-on-unknown (`monitor.py:728-762`, `monitor_workflow.py:186-194`).
3. Set explicit `id_reuse_policy` everywhere `start_workflow` / `start_child_workflow` is called: `monitor_workflow.py:200-221`, `twitter_workflow.py:469-478`, `upload.py:73-85`.

### 1b. NameError in download.py — crashes vision-validation fallback

`src/activities/download.py:819` references `MODULE` but no `MODULE = "download"` constant exists in the file (only `upload.py:14` defines one). If `ffprobe` raises during AI validation, the except-branch tries to log via `MODULE` and crashes with `NameError` — exactly the failure mode the try/except is supposed to handle gracefully.

### 1c. NameError in cleanup_old_fixtures

`src/activities/ingest.py:342` — `except Exception as e: ... log.warning(... key=obj['Key'])`. `obj` is undefined at this scope (it was the loop variable). Latent — fires only when the S3 delete-object call inside the loop raises.

### 1d. `get_team_info` has no timeout — hangs forever

`src/api/api_client.py:240` — `requests.get(url, ..., params={"id": team_id})` is the only API call in the module without `timeout=`. If API-Football stalls (CL nights, their incidents), this hangs until the worker is killed.

### 1e. S3 dedup `break` (already documented)

`src/activities/upload.py:614-618` — covered in `docs/todo.md`. Extra detail from audit: even when `_old_s3_key` is set on the new video (line 640), only the **first** match's key survives; if a later S3 video would have matched perceptually, its key is silently lost on the in-place replacement.

### 1f. MD5-dedup silent miss from `s3_key` vs `_s3_key` mismatch

`src/activities/upload.py:352` — reads `s3_video.get("s3_key", "")` but the field is consistently keyed as `"_s3_key"` everywhere else (set at upload.py:182, read by `deduplicate_videos`). The `or s3_url.replace("/video/footy-videos/", "")` fallback rescues the common case, but any URL not matching that exact prefix produces a silent MD5-dedup bypass.

### 1g. Malformed `$pull` in `update_video_in_place`

`src/activities/upload.py:945` — `{"$pull": {"events.$._s3_videos": {"url": old_s3_url}}}`. The positional `$` inside `$pull` refers to the matched element of `events` (outer array), not the matched element of `_s3_videos` (inner array). Depending on Mongo's tolerance this either silently no-ops or pulls every video. Needs careful test.

### 1h. Three non-atomic read-then-write patterns in mongo_store.py

All three can race when multiple workflows hit the same event simultaneously:

- `mongo_store.py:660-703` `add_drop_workflow_and_check` — `$addToSet` then `find_one`. Two MonitorWorkflows can both observe `count == 3` and double-delete the event.
- `mongo_store.py:803-849` `check_and_mark_download_complete` — `get_count` → `find_one` → `mark_complete`, three round-trips, no atomicity.
- `mongo_store.py:248-253` `activate_fixture_with_data` — deletes from staging *before* asserting the active replace succeeded. If replace fails, fixture is gone from staging and never lands in active.
- `mongo_store.py:1411-1441` `complete_fixture` — insert-to-completed then delete-from-active. Worker crash between the two leaves the fixture in BOTH collections, violating the 5-collection invariant.

**Pattern to use:** `findOneAndUpdate` with `$expr` size check or a single pipeline-update.

### 1i. `start_workflow + start_signal` silently drops upload batches

`src/activities/upload.py:73-85` (`queue_videos_for_upload`) uses
`client.start_workflow(..., start_signal="add_videos", ...)`. Without an
explicit `id_reuse_policy`, if a previous UploadWorkflow for that event
already completed (e.g., the 5-min idle timeout fired before the 10th
DownloadWorkflow's signal arrived), the next signal attempt raises
`WorkflowAlreadyStartedError`. That's caught at lines 96-102, logged as
`status="error"`, and the **videos are silently dropped**. Use
`signal_with_start_workflow` (the SDK helper handles already-running cleanly)
and/or set `id_reuse_policy=ALLOW_DUPLICATE` explicitly.

### 1j. `asyncio.gather` in MonitorWorkflow has no `return_exceptions=True`

`src/workflows/monitor_workflow.py:254` — one fixture's activity exception
kills the entire 30 s monitor cycle for all other fixtures. With SKIP overlap
policy, the next cycle proceeds, but in-flight fixture state can be partially
updated. Add `return_exceptions=True` + per-fixture try/except with
structured error logging.

### 1k. `_check_and_mark_download_complete` skipped on early-return path

`src/workflows/upload_workflow.py:712-742` is only invoked on the idle-timeout
branch (line 106). The `event_removed` early-return (line 132-140) skips it.
An event with 10 registered downloads that gets VAR'd at the wrong moment
never gets `_download_complete=true` set via this path.

---

## 2. ⚠️ Lower-urgency bugs (real, but haven't bitten yet)

- `src/workflows/twitter_workflow.py:506` — `'download_count' in dir()` guard is always True once `download_count` is bound (incl. the `except: download_count = 0` at line 270). The defensive check does nothing. Replace with `download_count = 0` before the while loop.
- `src/workflows/twitter_workflow.py:457-459` — DownloadWorkflow ID uses `team_aliases[0]` which can re-order across RAG runs. If TwitterWorkflow re-spawns (suspects a/b), the new run may pick a different alias-zero → distinct DownloadWorkflow IDs for `attempt=1` → duplicates within `_download_workflows`.
- `src/activities/monitor.py:574` — `clear_drop_workflows` runs unconditionally on every MATCHING event, every cycle. Means an event that flickers MISSING-PRESENT-MISSING never gets dropped because the count resets on each PRESENT.
- `src/activities/monitor.py:615` — `new_count_val = min(current_count + 1, 3)` clamps at 3, but `_monitor_workflows` is `$addToSet` so its length keeps growing past 3. The two counters drift apart over a long fixture.
- `src/activities/monitor.py:521-522, 583, 609, 616` — Still writing the DEPRECATED `MONITOR_COUNT` field. `mongo_store.py:1400`'s completion check reads it. Pick a side and finish the migration.
- `src/activities/rag.py:42-44` and `download.py:25` — Each defines `Semaphore(2)` per worker process. With 8 worker replicas × 2 activity types = up to 32 concurrent LLM calls. AGENTS.md says hard cap is 2. Either AGENTS.md is wrong, the comment is wrong, or production is overshooting joi.
- `src/utils/event_config.py:90` — `DEBOUNCE_STABLE_COUNT = 3` at module bottom is a separate source of truth from the per-event `debounce_stable_count: 3` inside `EVENT_TYPES`. Will drift on tweaks.
- `src/scaler/scaler_service.py:38` — `PROJECT_NAME = "found-footy-prod"` hardcoded. If anyone runs this scaler container in dev, it scales prod containers. Should come from `COMPOSE_PROJECT_NAME`.
- `src/scaler/registry.py:53` — `TWITTER_SESSION_URL` default `http://found-footy-prod-twitter:8888`. Same hardcoded-prod issue.
- `src/data/mongo_store.py:75-108` — `_create_indexes` swallows all errors with a single try/except around 12 index creations. If index N-1 fails, N..12 silently skip and `_indexes_created = True` is never set → next instance retries the whole batch. Move the flag set into a `finally`.
- `src/data/mongo_store.py:113-144` — Three static helpers (`_extract_fixture_id`, `_extract_status`, `_extract_current_goals`) catch bare Exception and return defaults (`0`, `"NS"`, `{home:0, away:0}`). A malformed API payload looks like "fixture 0, no goals" → guaranteed silent downstream breakage.
- `src/data/s3_store.py:104` — Non-ASCII metadata silently replaced with `?`. Diacritics in team names → corrupted tags.
- `src/data/mongo_store.py` — 47 `except Exception` blocks of pattern `log + return None/[]/False`. Failed queries indistinguishable from empty results at call sites.
- `src/api/api_client.py:96-101` — API errors logged as warning then ignored. Batch fixtures silently return partial results.
- `src/workflows/ingest_workflow.py:269-307` — RAG pre-cache loops sequentially with 90s timeout per team × ~111 teams = up to 2.7 hours worst case. Should `asyncio.gather` in batches of 2 (joi cap).
- `src/workflows/ingest_workflow.py:192-214` — Empty-day lookahead loop can hang ingest for >10 min on a quiet day. Fail-fast or batch via `fixtures_batch`.
- `src/activities/ingest.py:319-339` — S3 orphan-prefix cleanup uses `list_objects_v2` without pagination. Silently deletes only the first 1000 keys for large fixtures.

---

## 3. 🧹 Dead code (delete with confidence — ~900 lines total)

Nothing on this list is called from production code paths. All confirmed via grep.

### Whole files
- **`src/workflows/rag_workflow.py`** (217 lines) — `RAGWorkflow` is registered in `worker.py:187`, exported from `workflows/__init__.py`, but **never invoked**. Would `TypeError` if invoked because it passes `team_aliases=` to `TwitterWorkflowInput`, which has no such field. Header docstring says "Team Alias Lookup via Ollama LLM" — Ollama isn't used.
- **`twitter/auth.py`** (298 lines) — `TwitterAuthenticator` class used only by `twitter/manual_login.py`. Implements automated login (Twitter detects this pattern and bans accounts) and uses `pickle` cookies while `session.py` uses JSON cookies. Two parallel cookie systems, only the JSON one is on the hot path.
- **`twitter/manual_login.py`** — overlapped by `twitter/firefox_manual_setup.py`. Pick one.

### Activities registered in worker.py but never called by any workflow
- `src/activities/monitor.py:53-86` `fetch_staging_fixtures`
- `src/activities/monitor.py:674-696` `sync_fixture_metadata`
- `src/activities/monitor.py:802-857` `register_monitor_workflow`
- `src/activities/upload.py:891-959` `replace_s3_video` — superseded by `update_video_in_place`

### Module-level globals never referenced
- `src/activities/download.py:26-27` `_download_lock`, `_last_download_time` — vestiges of an old rate-limiter
- `src/activities/download.py:21` — comment describing the dead globals

### Functions defined but never called
- `src/data/models.py:67-141` — `FixtureStatus`, `EventType` enums (parallel to `src/utils/fixture_status.py` which everyone actually uses)
- `src/data/models.py:148-484` — All raw API TypedDicts (`APIStatus`, `APIFixture`, `APIVenue`, `APILeague`, etc.) — pure documentation, never imported anywhere
- `src/data/models.py:358-384` — `VideoFields` constants class
- `src/data/models.py:683-697` — `create_event_id()` helper — `store_live_fixture` builds the same string inline
- `src/data/models.py:793-801` — `TeamType` enum
- `src/data/s3_store.py:156-195` — `upload_video_file`
- `src/data/s3_store.py:197-221` — `list_goal_videos`
- `src/data/s3_store.py:223` — `get_existing_video_hashes`
- `src/data/s3_store.py:258` — `get_bucket_stats`
- `src/data/s3_store.py:325-364` — `tag_fixture_directory`, `get_video_tags`
- `src/data/mongo_store.py:197-199` — `fixtures_insert_staging` wrapper
- `src/data/mongo_store.py:1541-1557` — `delete_team_alias`, `clear_team_aliases`
- `src/data/mongo_store.py:1601-1607` — `clear_top_flight_cache`
- `src/scaler/scaler_service.py:313-323` — `scale_up`, `scale_down` "legacy compatibility"
- `src/scaler/scaler_service.py:393-407` — `scale_twitter`, `scale_workers` "legacy interface"
- `src/scaler/scaler_service.py:516-552` — `get_healthy_twitter_urls`
- `src/scaler/registry.py:182-203` — `mark_instance_busy`, `mark_instance_available`
- `src/utils/team_data.py:174-176` — `get_legacy_uefa_ids`
- `src/utils/team_data.py:160-166` — `get_uefa_team_ids` (duplicate of `get_top_flight_team_ids`)
- `src/api/api_client.py:271-331` — `test_events_api_debug` debug function with hardcoded fixture ID 1378993 left in production module

### Unreachable code branches
- `src/activities/download.py:899-935` — Text-fallback parser in `parse_response`. With `response_format={"type":"json_object"}` enforced at line 1414, JSON parse never fails. Includes the dead `SKIP` keyword branch (line 903-905) referencing a prompt that no longer exists.
- `src/activities/download.py:85-219` — `parse_broadcast_clock` only used by tests/scripts. Production path uses `parse_clock_field` after the prompt split into CLOCK/ADDED/STOPPAGE_CLOCK.
- `src/activities/upload.py:1429-1467` — `_parse_perceptual_hash` only used by the legacy-format branch of `_perceptual_hashes_match` (line 1318-1334). Production HASH_VERSION is locked to `dense:0.25`; no live producer of the legacy format remains.
- `src/activities/upload.py:1153-1170` — `_hamming_distance` only used by the same dead legacy branch. `_dense_hashes_match` inlines the operation.

### Duplicate imports
- `src/activities/download.py:7-8` re-imported at lines 548, 1487
- `src/activities/twitter.py:16, 19` — `import os` twice
- `src/worker.py:32, 48` — `from datetime import timedelta` twice

### Schema fields declared but never written
- `src/data/models.py:319, 322` — `VIDEO_COUNT`, `DOWNLOAD_STATS`. The `DownloadStats` TypedDict has 18 fields. Nothing writes them.
- `src/data/models.py:312-314, 352-354` — `MONITOR_COUNT`, `TWITTER_COUNT` marked DEPRECATED but still actively written (see §2).
- `src/data/models.py:262` — `LAST_MONITOR` documented in `FixtureFields.all_enhanced()` as universal but only ever written for staging.

### Configuration that lies
- `src/data/mongo_store.py:46-54` claims 5 collections; actually creates 6 (including `top_flight_cache`). Architecture doc says 5.
- `src/data/mongo_store.py:5` says "4-collection architecture"; line 47 says 5; reality is 6.
- `src/data/models.py:22` docstring says "4 MongoDB collections".
- `src/worker.py:246` startup banner says "Activities: 42 total (4 ingest, 10 monitor, 3 rag, 6 twitter, 7 download, 12 upload)" — actually 6 download, 6 listed under download but one of them (`queue_videos_for_upload`) is in upload.py.
- `src/data/mongo_store.py:60-61` checks both `MONGODB_URI` and `MONGODB_URL` env vars. Historical drift.

### Tests that are broken
- `tests/test_rag_pipeline.py:27` — imports `get_uefa_teams`, `get_fifa_teams` from `src.utils.team_data` — **neither function exists**. File fails at import. Also references nonexistent `found-footy-dev-ollama` container and `http://ollama-server:11434`.
- `tests/test_simple_wikidata.py:8-24` — Hardcoded copy of `TOP_UEFA` dict that drifts independently from `team_data.py`. No assertions; exploratory only.

---

## 4. 📦 Dependency hygiene

`requirements.txt` has **no version pins** except `undetected-chromedriver>=3.5.0` and `python-on-whales>=0.60.0`. `temporalio`, `selenium`, `boto3`, `fastapi`, `pymongo`, `pillow`, `opencv-python` — all unpinned. Container rebuilds can silently break.

### Confirmed unused (delete from requirements.txt)
- `undetected-chromedriver>=3.5.0` — Chrome replaced by Firefox/geckodriver
- `mutagen` — audio metadata library, never imported
- `psutil` — never imported
- `pyOpenSSL` — never imported

### Confirmed missing
- `httpx` — imported in download.py, rag.py, test_all_videos_validation.py but **not declared** in requirements.txt. Only present transitively via fastapi/uvicorn[standard].

### Pinning recommendation
Either `pip freeze > requirements.lock` to capture the current working set, or move to `uv`/`pip-tools` for proper lockfile management. The existing `uv.lock` is 190 KB and stale.

---

## 5. 🏗️ Refactor opportunities

Sized S/M/L. Don't tackle until §1 and §3 are done.

### S — small, ~hours, low risk
- **Stabilize Twitter workflow ID** — drop minute/extra/team/player, use `f"twitter-{event_id}"`. Fixes Lazio Pisa (a).
- **Expand `check_twitter_workflow_running` block-set** — treat all terminal failure states as "don't restart"; return `"unknown"` on RPC errors. Fixes Lazio Pisa (b).
- **Set explicit `id_reuse_policy` everywhere** — all `start_workflow` / `start_child_workflow` calls.
- **Replace `start_workflow + start_signal` with `signal_with_start_workflow`** in `queue_videos_for_upload`.
- **Add `return_exceptions=True` to `asyncio.gather`** in `monitor_workflow.py:254`.
- **Module-level singletons for `FootyMongoStore` + `FootyS3Store`** — 29 instantiations across activities; each builds a new connection pool. One-time fix, big perf+CPU win.
- **Auto-derive activity count in worker startup banner** — `len(worker.activities)` instead of the hardcoded string.
- **Delete dead globals + dead `_download_lock`/`_last_download_time` in download.py.**
- **Pin requirements.txt and remove confirmed unused packages.**

### M — medium, ~1-2 days, moderate scope
- **Atomic completion-tracking ops** — replace the four read-then-write patterns in mongo_store.py with `findOneAndUpdate` + `$expr` size checks. Eliminates 4 race classes.
- **Centralize orchestration constants** — `MAX_ATTEMPTS`, `REQUIRED_DOWNLOADS`, `MAX_VIDEOS_TO_DOWNLOAD`, `DROP_THRESHOLD`, `IDLE_TIMEOUT_M`, `ATTEMPT_SPACING_S` into `src/utils/orchestration_config.py`.
- **Retire `MONITOR_COUNT`/`TWITTER_COUNT` writes** — stop writing from monitor.py; update completion-check reader in mongo_store.py to use array length; drop `update_event_stable_count`.
- **Extract shared clock-parsing into `src/utils/clock_parsing.py`** — used by both `download.py` and `scripts/test_structured_extraction.py` (which currently maintains its own copy).
- **Replace `_country_variations` LLM call with static dict** in `rag.py` — ~50 footballing countries, deterministic, removes an unbounded cache.
- **Audit Twitter scraper selector fragility** — `twitter/session.py:670` uses `.r-1e081e0` (a Twitter atomic CSS class regenerated on each deploy). Replace `time.sleep` waits with `WebDriverWait`. Add `/health/scrape-readiness` endpoint with a canary search.

### L — large, multi-session, structural
- **Split `src/activities/download.py` (1672 lines)** — into `download.py` (core download, ~600), `vision.py` (AI validation + clock extraction, ~500), `hashing.py` (perceptual hash — sized for deletion under qwen-embeddings, ~200).
- **Split `src/activities/upload.py` (1467 lines)** — extract `_perceptual_match_helpers` into `src/utils/dedup.py` (also sized for deletion under qwen-embeddings).
- **Split `src/data/mongo_store.py` (1608 lines)** — by domain: `fixtures.py`, `events.py`, `videos.py`, `aliases.py`, `cache.py`.
- **Replace dual-tracking (counters + workflow-ID arrays)** — pick workflow-IDs everywhere, delete the counters, document the migration.

---

## 6. 📚 Stale docs / comment / config references

Worth fixing as a sweep, low priority individually.

- `src/workflows/rag_workflow.py:2` — "Team Alias Lookup via Ollama LLM" (Ollama not used, file is dead anyway — will be deleted per §3)
- `src/data/mongo_store.py:507-509` — note about `_twitter_count` (deprecated field)
- `src/data/mongo_store.py:438-444` — docstring for `add_event_to_active` lists fields it doesn't actually set
- `src/data/models.py:875` — example uses `"model": "qwen3-vl:8b-instruct"` but env default is `"Qwen3-8B"`
- `src/workflows/monitor_workflow.py:226-230` — long migration-explanation comment block; migration is complete
- `src/workflows/twitter_workflow.py:16-22` — block comment documenting the "OLD: Counter → NEW: Array" migration; complete
- `src/workflows/monitor_workflow.py:218-220` + `twitter_workflow.py:474-476` — same "task_timeout=60s" rationale repeated verbatim
- `src/workflows/upload_workflow.py:443-445` and `twitter_workflow.py:513-515` — both misattribute who handles temp-dir cleanup
- `src/activities/monitor.py:494, 520` — "OLD APPROACH (counter-based)" + "OLD: Decrement the counter (kept for backwards compatibility, not used for decisions)" — but counter is still written
- `src/activities/download.py:21, 432-434, 477, 954, 1539-1550` — assorted stale comments (full list in agent output)
- `src/activities/upload.py:192, 809` — "Phase 1" markers (no longer phased)
- `src/activities/upload.py:1287-1294` — "Legacy format" comment for code with no live producer
- `twitter/__init__.py:9` — describes `auth.py` as the authentication path (it's dead)
- `twitter/README.md:13, 54, 206` — `localhost:4203/3203` references contradict the AGENTS.md "Caddy-only" policy
- `twitter/manual_login.py:8, 27, 38, 47, 63, 64` — `localhost:6080`, `localhost:3103` stale ports
- `twitter/test_service.py:13` — `BASE_URL = "http://localhost:3103"` wrong port
- `tests/workflows/test_ingest.py:94-96` + `tests/workflows/test_pipeline.py:151-153` — `http://localhost:4100/4101` stale URLs
- `src/scaler/scaler_service.py:9-13` vs `:47` — docstring says "~2-3 concurrent goals", constant is `2`
- `src/utils/config.py:22-23` — commented-out `LLAMA_EMBED_URL` (will be uncommented when qwen-embeddings work begins)

---

## 7. 🎯 Magic numbers worth externalizing

Group these into `src/utils/orchestration_config.py` (orchestration tunables) and `src/utils/dedup_config.py` (vision + dedup tunables) — see §5 M-sized refactor.

- LLM concurrency: `Semaphore(2)` in download.py:25 and rag.py:44
- Vision sample fractions: `0.25, 0.50, 0.75` in download.py:938-939, 1010
- Clock tolerance: `±1` in download.py:383, 401 and `±3` in `validate_timestamp`
- Vision confidence scores: 0.7, 0.95, 0.90, 0.85, 0.6 in download.py:1028-1047
- ffmpeg/ffprobe timeouts: `10s` in download.py:815, 1357, 1505, 1587
- Default video duration fallback: `10.0s` in download.py:821
- Minimum download bytes: `1000` in download.py:737
- Syndication API timeout: `5.0s` in download.py:581
- CDN download timeout: `60.0s` in download.py:700
- dHash dimensions: `(9, 8)` in download.py:1597
- MD5 prefix length: `8` in upload.py:361, 733, 738
- S3 bucket name: `"footy-videos"` in upload.py:930
- `DURATION_SIMILARITY_THRESHOLD = 0.15` (named, but at upload.py:1174 between unrelated functions — move to config)
- Twitter workflow: `MAX_ATTEMPTS=15`, `REQUIRED_DOWNLOADS=10`, `MAX_VIDEOS_TO_DOWNLOAD=5`, `max_age_minutes=3`, `wait_seconds = max(60 - elapsed, 10)` — all inline in twitter_workflow.py
- Monitor: `DROP_THRESHOLD=3` in mongo_store.py:678, `current_interval = (now.hour * 4) + (now.minute // 15)` in monitor.py:124, `lookahead_minutes=30` default in monitor.py:90
- Upload: `idle_timeout = timedelta(minutes=5)` in upload_workflow.py:90, `task_timeout=60` in upload.py:84
- Twitter health cache TTL: `30s` in twitter.py:283
- Wikidata scoring weights: `+200/+100/+50` in rag.py:294-302
- Alias length cap: `40` in rag.py:398
- Wikidata HTTP timeouts: `10s, 10s, 10s, 45s` (inconsistent) across rag.py
- `Bucket="footy-videos"` repeated 4× and `/video/footy-videos/` path prefix repeated 4× in upload.py

---

## Recommended ordering

Don't do this in one sprint. Suggested split:

1. **Sprint 1 (1 session, ~3h)** — Fix Lazio Pisa cluster + the 4 NameError/silent-fail bugs (§1a + §1b-1d). Highest leverage, smallest surface.
2. **Sprint 2 (1 session, ~3h)** — Fix the 4 atomicity bugs (§1h + §1i + §1k). Mongo correctness sweep.
3. **Sprint 3 (1 session, ~2h)** — Dead code purge (§3) + dependency hygiene (§4). Mechanical, high satisfaction.
4. **Sprint 4 (1 session, ~3h)** — Connection singletons + centralize orchestration constants (§5 S-sized batch).
5. **Sprint 5 (multi-session)** — Module splits + counter retirement (§5 M and L).
6. **Sprint 6+** — Feature work (qwen-embeddings, event-matching follow-through, geo-restriction).
