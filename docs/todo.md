# Active TODO

Paste-ready start-of-session block. Newest items above older.

> **Companion doc**: a full code audit was taken in May 2026 and lives in
> `docs/audit.md`. It catalogs ~50 specific findings with file:line refs and a
> suggested 6-sprint ordering. Items below cite back to it.

---

## Open bugs (priority order)

### ✅ Sprint 1 SHIPPED — Lazio Pisa cluster + correctness sweep (2026-05-26)

**Status**: complete in commits c44e3bb → 7a48e0f. All 87 tests pass.

Net change: 6 commits, +442/−413 lines, 9 files touched. Net new architecture
properties:
- Workflow IDs stable: `f"twitter-{event_id}"` only (no API-mutable fields).
- Server-enforced dedup via `id_reuse_policy=REJECT_DUPLICATE` on Twitter +
  Download spawns — `check_twitter_workflow_running` activity DELETED entirely
  (~80 lines, replaced by Temporal's own dedup).
- `_download_complete` owned by `DownloadWorkflow`'s try/finally — last DLWF
  to finish flips the flag. "Always signal empty list" workaround removed.
  UploadWorkflow's idle-timeout failsafe retained for the all-DLWFs-crashed case.
- `id_reuse_policy=ALLOW_DUPLICATE` on UploadWorkflow start so late-batch
  signals after Completed UploadWorkflow can spawn a fresh instance.
- New `src/utils/temporal_client.py` — process-wide singleton replacing the
  per-call `Client.connect` pattern. ~100+ extra gRPC connects per CL night
  eliminated.
- Latent bugs fixed: `MODULE` NameError at `download.py:819`, missing timeout
  on `get_team_info`, dead `'download_count' in dir()` guard.

The Lazio v Pisa stuck event was manually unstuck on 2026-05-24. With Sprint 1
deployed, the same failure mode shouldn't recur on future fixtures.

**Audit false positive noted**: §1c claimed an `obj` NameError at
`ingest.py:342` — verified false. Line 342 uses `prefix=prefix, error=str(e)`,
not `obj`. The inner `obj` reference at line 338 is the for-loop variable from
line 327 which is correctly defined. No change made.

### 🔥 Critical correctness bugs (audit §1)

All from `docs/audit.md` §1; same severity tier as the Lazio Pisa bug.

- **NameError crash in vision-validation fallback** — `download.py:819` uses undefined `MODULE` constant. Crashes the activity on any ffprobe error during AI validation.
- **NameError in cleanup_old_fixtures** — `ingest.py:342` references undefined `obj` in the except branch. Latent.
- **`get_team_info` has no timeout** — `api_client.py:240`. Hangs forever if API stalls.
- **MD5-dedup silent miss** — `upload.py:352` reads `s3_key` but field is `_s3_key`. Silent dedup bypass for URLs not matching exact prefix.
- **Malformed `$pull` query** — `upload.py:945`. Positional `$` refers to wrong array; silent no-op or over-pull.
- **Three non-atomic Mongo read-then-write patterns** — `mongo_store.py:660-703, 803-849, 248-253, 1411-1441`. Race conditions; fixture can land in zero or both collections.
- **`signal_with_start` substitute drops batches silently** — `upload.py:73-85` uses `start_workflow + start_signal` instead of `signal_with_start_workflow`. After idle-timeout of the previous UploadWorkflow, the next signal raises `WorkflowAlreadyStartedError` and the batch is silently dropped.
- **`asyncio.gather` without `return_exceptions=True`** — `monitor_workflow.py:254`. One fixture's failure poisons the entire monitor cycle.
- **`_check_and_mark_download_complete` skipped on event-removed path** — `upload_workflow.py:712-742`. Edge case where VAR'd events with 10 registered downloads never get marked complete via this path.

### S3 dedup only matches the first existing video

`src/activities/upload.py:614-618` — `for existing in existing_videos_list: ... break`. If an event already has 3 perceptually-equivalent S3 videos and a new one arrives, only the first match is replaced/popularity-bumped; the other two stay as zombies. Design in @docs/proposals/dedup-unification.md (drop the `break`, collapse all matches, introduce `_video_redirects` for URL stability).

### ⚠️ Other bugs that haven't bitten yet

See `docs/audit.md` §2 for the full list. Highlights:
- LLM concurrency may exceed joi's hard cap of 2 — `Semaphore(2)` is per-process so 8 worker replicas × 2 activity types = up to 32 concurrent.
- Scaler hardcoded `PROJECT_NAME="found-footy-prod"` — running scaler in dev would scale prod containers.
- `_create_indexes` swallows all errors in one try/except — partial-failure leaves later indexes missing forever.
- API client silently returns partial results on error; `mongo_store.py` has 47 `except Exception: log + return [] / False / None` patterns that make failures indistinguishable from "no data".

---

## Feature work

### LLM stack redesign (embedding-augmented vision + concurrency gateway)

See @docs/proposals/llm-stack-redesign.md. Three independent tracks:
- **Track 1** (unblocked): build a workspace LLM gateway in front of joi for global concurrency + priority + backpressure metrics.
- **Track 2** (mostly unblocked): cut per-video chat calls 2-3 → 1 by sending all frames in one multi-image call; move RAG alias selection from `llama-small` to `llama-large`.
- **Track 3** (blocked on serving stack): swap perceptual hashing AND soccer/phone-cam classification to Qwen3-VL-Embedding-8B image embeddings. llama.cpp doesn't yet support this model on AMD/Vulkan; three sub-paths (test community GGUF / sentence-transformers fallback / wait for upstream).

### Re-attribution recovery on scorer changes

See @docs/proposals/event-matching.md. **Active on this branch
(`feature/event-matching`)** — the original goal-scorer attribution from
the API sometimes changes (assist-to-goal flip, own-goal re-assigned to
the deflecting defender, etc.). We want to transfer the already-downloaded
videos to the new event ID rather than starting over.

### Geo-restricted broadcaster CDN bypass

See @docs/proposals/geo-restriction-bypass.md. Not started. Symptoms:
some videos (DSports, somos_FOX, S Sport Turkey, TNT Sports) 403 on the
download because they're geo-fenced. Proposal is a multi-region proxy
pool keyed on the broadcaster's confirmed region.

---

## 📋 Joi-state corrections (logged 2026-05-24, lightweight)

The LLM stack redesign proposal (`docs/proposals/llm-stack-redesign.md`) and
the user-global `~/.claude/CLAUDE.md` both have outdated joi model info.
Captured here so they get fixed when those docs next get touched (full
rewrite deferred — not urgent if the VL embedding swap is in motion):

- **Chat model at port 3101** is `Qwen3-VL-30B-A3B-Instruct Q8_0` (31 GB MoE
  with vision via `mmproj-F16.gguf`), NOT `Qwen3.5-122B-A10B` as global
  CLAUDE.md claims.
- **Port 3103 currently runs SigLIP** (ViT-B/16-224, 768-dim image
  embeddings, CPU PyTorch via `siglip-embed/`) — being retired during the
  Qwen3-VL-Embedding-8B swap because it never produced useful output for
  found-footy.
- **No `llama-large/small/embed.joi` Caddy hostnames** are visible in the
  synced `~/workspace/llamacpp/` directory. The synced README documents
  direct port access via the Tailscale FQDN. The Caddy hostnames are used
  in found-footy's `.env` (`LLAMA_URL=http://llama-small.joi`) and must
  live in a joi-side directory not in the local sync. Worth confirming
  with the joi operator and either adding to the sync or documenting
  separately.
- **For the upcoming Qwen3-VL-Embedding-8B swap**: the mmproj from
  `Qwen/Qwen3-VL-8B-Instruct-GGUF` (`mmproj-Qwen3VL-8B-Instruct-F16.gguf`)
  is the candidate pair for the dam2452 embedding GGUF — both built on
  Qwen3-VL-8B-Instruct, same vision tower. Validate via the Path B test
  script (`scripts/test_qwen_vl_embedding.py`) before relying on it.

## 🧹 Dead code purge (audit §3)

~900 lines of confirmed dead code with no production callers. See `docs/audit.md` §3 for the line-by-line list. Highlights:
- Whole files: `src/workflows/rag_workflow.py` (217 lines, would TypeError if called), `twitter/auth.py` (298 lines, dangerous automated-login path)
- 4 registered-but-uncalled activities (replace_s3_video, fetch_staging_fixtures, sync_fixture_metadata, register_monitor_workflow)
- Lots of dead enums, helpers, TypedDicts in `models.py`, `mongo_store.py`, `s3_store.py`, `scaler/`
- `tests/test_rag_pipeline.py` is broken at import (missing functions + nonexistent Ollama container)

## 📦 Dependency hygiene (audit §4)

- requirements.txt has **no version pins** — container rebuilds can silently break.
- 4 packages declared but never imported: `undetected-chromedriver`, `mutagen`, `psutil`, `pyOpenSSL`
- `httpx` is imported but not declared (transitively pulled by fastapi/uvicorn)

## Doc cleanup (low priority)

- `docs/rag.md` lines ~460-530 and ~840-870: pseudo-code stubs referencing non-existent `OLLAMA_URL` / `OLLAMA_MODEL` constants. Doc header already flags as design-stage; replace stubs with pointers to `src/activities/rag.py`.
- `docs/architecture.md` claims 5 collections; actual is 6 (`top_flight_cache` missing from the list).
- `docs/logging.md` is 1285 lines — comprehensive but could split into "schema reference" + "Loki query cookbook".
- Worker startup banner activity count is off by one (`src/worker.py:246`) — should auto-derive from `len(worker.activities)`.
- Various stale comments / port references catalogued in `docs/audit.md` §6.

## 🏗️ Refactor opportunities (audit §5)

Don't tackle until §1 + §3 are done. Suggested ordering in `docs/audit.md` §5.
Sized S (hours), M (1-2 days), L (multi-session). Highlights:
- **S**: Module-level singletons for FootyMongoStore + FootyS3Store (29 instantiations across activities → one connection pool each)
- **M**: Atomic completion-tracking ops, centralize orchestration constants, retire `MONITOR_COUNT`/`TWITTER_COUNT` writes
- **L**: Split `download.py` (1672), `upload.py` (1467), `mongo_store.py` (1608) by domain
