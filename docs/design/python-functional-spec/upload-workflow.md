# Upload workflow — Python behavior spec

Frozen WHAT-and-WHY detail from the
[Python functional-spec index](./README.md).

## Upload Workflow — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/workflows/upload_workflow.py`, `archive/src/activities/upload/{core.py,dedup.py}`.

### 1. Signal-based FIFO pattern

**PURPOSE:** Serialize all S3+MongoDB writes for a single event through one Temporal workflow so concurrent DownloadWorkflows (DLWFs) cannot race each other on the shared dedup state.

**BEHAVIOR:**
- Per-event workflow ID `upload-{event_id}` (core.py:110); different events run in parallel.
- `signal-with-start` from `queue_videos_for_upload` (core.py:124–147): starts a new UploadWorkflow if none is running, otherwise delivers the signal to the existing instance.
- `add_videos` signal appends batches to a `deque` (upload_workflow.py:66–78); the main loop's `workflow.wait_condition` (upload_workflow.py:97–100) unblocks when the queue is non-empty or the idle timeout fires.
- Temporal guarantees signal delivery order → FIFO processing (first DLWF to signal = first to upload).
- `id_reuse_policy=ALLOW_DUPLICATE` (core.py:143) lets a late signal arriving after a Completed workflow start a fresh instance rather than silently drop; documented as the fix for the Lazio–Pisa stuck-event symptom.
- Empty-batch signals suppressed at the activity (core.py:93–102): telemetry recorded, signal skipped.

**REMARKS:** The workflow *is* the mutex — chosen so concurrency safety is Temporal-native, no DB row lock required. Fresh S3 state is fetched *inside* the serialized section (upload_workflow.py:197–201). The Go rewrite replacing idle-timeout with drain completion MUST preserve `ALLOW_DUPLICATE` for late-arriving signals.

### 2. Per-batch processing flow

**PURPOSE:** One `_process_batch` invocation fully absorbs one DLWF's videos into the shared S3+MongoDB state before the next batch is popped.

**BEHAVIOR:** In fixed order per `_process_batch` (upload_workflow.py:168–679):
1. Fetch fresh S3 state via `fetch_event_data` (line 206).
2. MD5 dedup — batch-internal + against S3 by filename hash (line 251).
3. Popularity bump for MD5 S3-exact matches (line 260).
4. Split MD5 replacements from perceptual candidates (lines 310–319).
5. Perceptual dedup, scoped verified↔verified and unverified↔unverified in parallel (line 343).
6. Popularity bumps for "existing was better" cases (line 412).
7. S3 uploads in parallel — new + replacements (line 469).
8. `save_video_objects` for new URLs (line 564).
9. `update_video_in_place` for successful replacements (line 595).
10. `recalculate_video_ranks` (line 623).
11. Frontend notify iff `videos_changed > 0` (line 640).
12. `_cleanup_uploaded_files` — individual files only, NOT the temp dir (line 659).

**REMARKS:** Fetching S3 state per-batch (not once per workflow) is what makes dedup race-free.

### 3. Verified vs unverified pools

**PURPOSE:** Prevent a verified goal clip from being replaced by an unverified clip of a *different* match moment that happens to look visually similar because it came from the same broadcast.

**BEHAVIOR:**
- After MD5 dedup, videos are partitioned by `timestamp_verified` (upload_workflow.py:327–328).
- Existing S3 videos partitioned the same way (lines 330–331).
- Two independent `deduplicate_videos` activities run concurrently via `asyncio.gather` (lines 343–358); verified pool sees only verified S3, unverified pool only unverified S3.
- If EITHER pool raises, both are zeroed and unverified `skipped_urls` is populated (lines 359–374) — the whole batch is skipped rather than uploaded-as-new.
- Results merged post-dedup (lines 377–393).

**REMARKS:** Same broadcast produces similar perceptual hashes across clock moments. A naive rewrite that folds both pools into one dedup pass regresses the exact bug the partition exists to prevent. The Go rewrite must keep the split.

### 4. Duration-vs-resolution winner picking

**PURPOSE:** Within a perceptual-duplicate cluster, disambiguate "same clip, trimmed differently" (prefer higher resolution) from "different clip of the same goal" (prefer longer).

**BEHAVIOR:**
- `DURATION_SIMILARITY_THRESHOLD = 0.15` (dedup.py:43).
- `_pick_best_video_from_cluster` (dedup.py:479–521): compute the longest as reference; for every cluster member compute `(max_dur − v.dur) / max_dur`.
- If ALL members are within 15% of longest → "similar" → pick highest `file_size` (line 517).
- If ANY member exceeds 15% → "different" → pick longest, `file_size` tiebreaker (line 521).

**REMARKS:** Percentage, not absolute seconds, because absolute breaks at both ends: 10s vs 15s = 50% (different clips) but 60s vs 65s = 8% (same clip). The constant was orphaned during the Phase 3 split (dedup.py:39–43) and silently `NameError`d during Crystal Palace v Rayo before being restored 2026-05-27 — keep it a named constant in Go, don't inline `0.15`.

### 5. Should-replace-S3 decision

**PURPOSE:** Extend the same 15% rule to a new candidate vs an already-uploaded S3 asset for quality-driven in-place replacement.

**BEHAVIOR:**
- `_should_replace_s3_video` (dedup.py:523–572).
- Duration-diff-pct ≤ 15% → replace only if `new_file_size > existing_file_size` (lines 560–567).
- Duration-diff-pct > 15% → replace iff new is longer (lines 568–572).
- Zero-duration fallback: file-size comparison (lines 547–551).
- Returns `(bool, reason_str)`; the reason threads into the log line (upload_workflow uses it via activity output at dedup.py:437–453).

**REMARKS:** The reason string is load-bearing for post-hoc Loki debugging — an operator must be able to tell why a specific replacement fired. Preserve reason strings in the Go port.

### 6. Popularity accumulation

**PURPOSE:** Track how many independent scraper sources found "this clip" so ranking favours widely-witnessed goals over one-shot uploads.

**BEHAVIOR:**
- Starts at 1 per successful download (dedup.py:109–110, 322–325).
- MD5-identical cluster of size N → survivor gets popularity N (dedup.py:114–116).
- Perceptual cluster → survivor gets `sum(popularity)` across the cluster (dedup.py:329–330), preserving popularity that MD5 dedup already accumulated.
- S3 match, existing wins → `existing.popularity += incoming.popularity` (dedup.py:449; upload_workflow.py:263–280).
- S3 match, new wins (replacement) → `new.popularity = old.popularity + incoming.popularity` (dedup.py:436; MD5 path dedup.py:188).
- Persisted via `bump_video_popularity` (core.py:549–585) OR baked into the video_object for `save_video_objects` / `update_video_in_place`.

**REMARKS:** Popularity survives replacement — this is *why* the in-place pattern (§7) exists. A "delete-then-insert" rewrite would zero the counter unless popularity is carried into the replacement candidate BEFORE the S3 PUT, not after.

### 7. Replace + absorb sequence

**PURPOSE:** Upgrade an existing S3 asset in place while keeping shared URLs valid and absorbing the old popularity atomically.

**BEHAVIOR:**
- Dedup marks the winning candidate with `_old_s3_key` (dedup.py:441–442).
- `upload_single_video` reuses `existing_s3_key` if provided (core.py:322, 351–356) — the S3 PUT overwrites the same key, so the URL is stable.
- MongoDB path uses `update_video_in_place` (core.py:477–547): positional `$set` at `events.{event_idx}.{S3_VIDEOS}.{video_idx}` (core.py:526–530) — one atomic update, no remove-then-add.
- Popularity is baked into `new_video_object` BEFORE the atomic write (upload_workflow.py:544–546).
- Frontend refresh fires for both new videos and in-place updates (upload_workflow.py:640).

**REMARKS:** The remove-then-add alternative was explicitly rejected — comment at upload_workflow.py:583–585 documents "avoids race where video disappears between remove and add". SSE consumers holding shared links would see a broken URL during the gap. Go rewrite: same key + atomic single-row update on Postgres's `video_asset` table.

### 8. Rank recalculation

**PURPOSE:** After any change to an event's video array, recompute a stable ordering so the frontend's `rank` field reflects current popularity and quality.

**BEHAVIOR:**
- Fires after every batch that produced new videos OR successful replacements (upload_workflow.py:622–634).
- Wraps `store.recalculate_video_ranks` (core.py:620–640); the ranking function itself lives in `mongo_store`.
- Max 2 attempts, logged as non-critical on failure (upload_workflow.py:632–634).
- Runs BEFORE frontend notify (line 640) so SSE consumers see fresh ranks on refresh.

**REMARKS:** Non-critical failure leaves ranks stale but data correct — the next batch's recalc catches up. Preserve the "non-fatal, retry-lite" posture in Go — don't fail the batch on rank failure.

### 9. VAR / event-removal handling

**PURPOSE:** Bail out cleanly when an in-flight event has been removed from the fixture (typically because API-Football revoked a VAR-overturned goal).

**BEHAVIOR:**
- `fetch_event_data` returns `{"status": "error", "error": "event_not_found"}` when the event is missing from `fixtures_active` (core.py:201–204).
- `_process_batch` short-circuits with `{"terminated_early": True, "reason": "event_removed"}` (upload_workflow.py:220–232).
- `run()` detects the flag and returns from the workflow immediately with terminal totals (upload_workflow.py:131–142) — no queue drain, no idle wait.
- Any remaining queued batches are discarded (they'd re-fail the same fetch anyway).

**REMARKS:** Hard terminate, not batch skip. A signal that arrives after termination will `ALLOW_DUPLICATE`-start a fresh workflow (§1), which will immediately re-detect and re-terminate — idempotent by design. Go rewrite: model as a domain-level typed error (e.g. `EventRemovedError`), not a magic-string check.

### 10. The 5-minute idle-timeout waste

**PURPOSE:** Document the flaw the Go rewrite is fixing — this is not a feature to preserve.

**BEHAVIOR:**
- After the 10th DLWF's batch drains, `workflow.wait_condition` (upload_workflow.py:97–100) sits waiting for a signal that will never come.
- The workflow blocks for `UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES` (line 92) — 5 minutes of Temporal worker occupancy per event, per goal.
- Only on timeout does it run `_check_and_mark_download_complete` as a failsafe (line 108; body 714–744) and then complete (line 156).

**REMARKS:** On a 20-goal Champions League night this is 100 wasted worker-minutes. Go contract: `dlwf_count == 10 AND queue empty → complete immediately`. Preserve the `_check_and_mark_download_complete` *intent* — that all 10 DLWFs are registered before the workflow exits — as an idempotent invariant enforced by the completion contract, not as a "run once on the way out" side effect.

### 11. Failure isolation

**PURPOSE:** One failed activity must not kill the workflow or lose queued batches; but a dedup failure MUST skip the batch, not upload duplicates.

**BEHAVIOR:**
- Top-level `try/except Exception` around `_process_batch` (upload_workflow.py:120–154): batch skipped, counter incremented, workflow survives to the next signal.
- `fetch_event_data` failure defaults `existing_s3_videos` to `[]` (lines 238–241) — degrades to "treat everything as new" (permissive).
- MD5 dedup failure defaults to passing all videos through (lines 294–298) — permissive.
- Perceptual dedup failure writes empty upload/replace lists AND fills `skipped_urls` (lines 359–374) — restrictive, batch's videos are DROPPED.
- Individual S3 upload failure caught per-task (core.py:511–515); other uploads in the batch proceed.
- Mongo save/update failures logged, non-fatal (lines 576–579, 610–614).

**REMARKS:** Two load-bearing invariants:

1. **Dedup-failure-skips-batch** (upload_workflow.py:360–374). The inline comment names the incident: uploading as-new when dedup errors caused a live duplicate-upload bug. A "fall back to uploading everything" rewrite regresses that fix.
2. **Cleanup does NOT touch the temp dir** (upload_workflow.py:446–447, 656–659, 685). TwitterWorkflow owns the temp lifecycle across all 10 DLWF attempts. Deleting the temp dir here would rip files out from under a still-running DLWF that shares the directory. The Go rewrite must keep upload-side cleanup at file granularity; directory cleanup stays with whoever owns the DLWF fan-out lifecycle.

---
