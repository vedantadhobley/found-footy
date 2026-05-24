# Active TODO

Paste-ready start-of-session block. Newest items above older.

---

## Open bugs (priority order)

### 🔥 Twitter workflow stuck "extracting" for hours

**Evidence**: 33′ goal in the recent Lazio v Pisa fixture (still visible
as "extracting" in the vedanta-systems frontend hours after the game
ended). Twitter is only supposed to search 10 times (~10 min after the
3-poll debounce completes).

**Suspect (a) — workflow-ID instability across API time updates**: in
`src/workflows/monitor_workflow.py`, the Twitter workflow ID is
`f"twitter-{team}-{player}-{minute_str}-{event_id}"` where
`minute_str = f"{minute}+{extra}min" if extra else f"{minute}min"`. If
the API mutates `minute` or `extra` between monitor cycles (e.g. 45 →
45+2 as stoppage extends), the workflow ID changes and
`check_twitter_workflow_running` looks up the new ID, finds nothing, and
spawns a fresh Twitter workflow. The original keeps running. Visible in
Temporal UI as multiple `twitter-*-{event_id}` workflows.

**Suspect (b) — `check_twitter_workflow_running` ignores failed/terminated
states**: `src/activities/monitor.py:700-762` only treats `RUNNING` and
`COMPLETED` as "don't restart". `FAILED` / `TIMED_OUT` / `TERMINATED` /
`CANCELED` fall through, so every 30 s monitor cycle re-fires Twitter if
it died once. Compounds the symptom.

**Suspect (c) — `_download_workflows` array filled but downloads never
completed**: ten `DownloadWorkflow`s register themselves at start via
`$addToSet`, so the count can reach 10 even if all of them subsequently
fail in AI-validation (joi unreachable, prompt response unparseable, etc).
With `_download_workflows.length == 10` and `_download_complete == false`,
no fresh attempts can fire — frontend shows "extracting" forever.

**Action**:
1. Pull from Temporal UI the listing of `*-{Lazio Pisa 33′ event_id}*` workflows, group by status. Multiple RUNNING + dead ones → (a). Many FAILED → (b) or (c).
2. Check the MongoDB event document — look at `_download_workflows`, `_monitor_complete`, `_download_complete`. If `_download_workflows` is at 10 but `_s3_videos` is empty/short → (c).
3. Fix: stabilize workflow ID (drop `minute_str`, keep only `event_id`); expand the "don't restart" set in `check_twitter_workflow_running` to include all terminal-with-failure states; consider a re-queue mechanism when registered-but-failed downloads exceed N.

### S3 dedup only matches the first existing video

`src/activities/upload.py:614-618` — `for existing in existing_videos_list: ... break`. If an event already has 3 perceptually-equivalent S3 videos and a new one arrives, only the first match is replaced/popularity-bumped; the other two stay as zombies. Design in @docs/proposals/dedup-unification.md (drop the `break`, collapse all matches, introduce `_video_redirects` for URL stability).

---

## Feature work

### Replace perceptual hashes with Qwen image embeddings

See @docs/proposals/qwen-embeddings.md. **Blocked** on confirming which
Qwen model to deploy on joi — current joi inventory has text embeddings
(`Qwen3-Embedding-8B` on `:3103`) but no image embedding model. Need
either a multimodal Qwen embedding (does one exist?), a CLIP/SigLIP
deployment, or a custom hook through the existing Qwen3-VL-8B.

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

## Doc cleanup (low priority)

- `docs/rag.md` lines ~460-530 and ~840-870: pseudo-code stubs that reference non-existent `OLLAMA_URL` / `OLLAMA_MODEL` constants. The doc header already flags itself as design-stage; the stubs should be replaced with brief pointers to `src/activities/rag.py`.
- `docs/architecture.md`: spot-check that activity counts and field tables match the current code after the recent refactors.
- `docs/logging.md` is 1285 lines — comprehensive but could probably split into "schema reference" + "Loki query cookbook".

---

## Code hygiene (tackle when touching the file for another reason)

- `src/activities/download.py` is 1672 lines (downloads + perceptual hashing + clock extraction + AI validation all mixed). Possible split: `download.py` (core download) + `hashing.py` (going away with embeddings) + `vision.py` (AI clock extraction).
- `src/activities/upload.py` is 1467 lines — similar story.
- `src/data/mongo_store.py` is 1608 lines — split by domain (fixtures vs events vs aliases) might help.
