# Proposal: Replace perceptual hashes with Qwen image embeddings

**Status**: Proposal — blocked on joi-side model deployment + threshold tuning.

## Motivation

Current dedup uses dense dHash perceptual hashing
(`_generate_perceptual_hash` in `src/activities/download.py:1533`):

- Sample frames every 0.25 s
- Histogram equalize each frame
- 64-bit dHash per frame, stored as `dense:0.25:<ts>=<hash>,<ts>=<hash>,...`
- Match by trying all time offsets between two clips, requiring 3
  consecutive frames at Hamming distance ≤ 10 bits (of 64)

This works but has known failure modes:

- **Cross-resolution matching is brittle.** Same clip at 480p vs 1080p
  often fails to match — histogram equalization is sensitive to
  compression artifacts.
- **Different camera angles don't match at all.** Each angle is a
  different visual signal in pixel space.
- **The 0.25 s sampling + all-pairs offset search is expensive** on long
  videos and at the all-pairs scale used in S3 comparison.
- **Threshold tuning is opaque.** `MAX_HAMMING_DISTANCE` and
  `min_consecutive_matches` are magic numbers. Tweaking them requires
  re-running the dedup pipeline at scale — we don't have a clean test
  harness for this.

## Proposed approach

Replace per-frame dHashes with **Qwen image embeddings** — a model that
produces a dense vector representation of each frame. Compare clips by
cosine similarity of either:

1. **A single pooled embedding per clip** (cheap, ignores temporal
   structure but may match different angles of the same moment), or
2. **A small ordered sequence of per-frame embeddings** (3–5 keyframes),
   compared pairwise like the current dense-hash offset search but in
   vector space.

Cosine similarity in a learned embedding space should:

- Be robust to resolution / compression / brightness shifts.
- Potentially cluster different camera angles of the same goal (depends
  on the model — the vision LLM probably understands that two angles
  show the same moment; pure embeddings might or might not — needs testing).

## Open questions (block this work)

1. **Which Qwen model?** Per `~/.claude/CLAUDE.md`, joi currently runs
   `Qwen3-Embedding-8B` on port `3103` — but that's a **text-only**
   embedding model. For image embeddings we need one of:
   - A multimodal Qwen embedding model (does one exist as of late 2026?
     Verify the Qwen release feed).
   - CLIP or SigLIP deployed on joi (not Qwen-branded but functionally
     equivalent — well-tested for cross-resolution / cross-angle similarity).
   - Use the existing Qwen3-VL-8B to produce embeddings via a custom hook
     (slower, but already deployed — pulls last-layer hidden states out of
     the vision tower).
2. **Caddy hostname on joi.** Suggest `llama-embed.joi` if a dedicated
   embedding model gets deployed (parallel to the existing
   `llama-small.joi`). Document the new entry in joi's caddy.d/.
3. **Vector storage.** Store embeddings as float arrays alongside
   `_s3_videos` in MongoDB (works at our scale — hundreds of vectors per
   match, ~thousands of retained matches before the 14-day cleanup). Or
   stand up a vector DB? **Recommendation: in-MongoDB with in-process
   cosine similarity is fine for this volume; don't over-engineer.**
4. **Similarity threshold.** Cosine threshold needs to be tuned on a
   labeled set. We have one — the existing S3 corpus, where clips
   flagged as duplicates by the current hash system are positive
   examples. Build a side-by-side comparison: for every (clip, S3
   sibling) pair the old system clustered, what's the cosine similarity?
   Pick a threshold that preserves the existing precision/recall balance.
5. **Migration strategy.** Existing S3 videos need backfilled
   embeddings. Either:
   - One-shot script (like the retired `migrate_perceptual_hashes.py`
     was for the dense-hash format) — clean cutover.
   - Lazy backfill on access — risk: dedup regressions during the
     transition window produce zombie duplicates.
   Lean toward one-shot. The 14-day retention means the corpus is small
   enough to backfill in an evening.

## Code surface

### `src/activities/download.py`

- `_generate_perceptual_hash` → `_generate_video_embedding` (calls
  `LLAMA_EMBED_URL` for each keyframe or for a pooled batch).
- `generate_video_hash` activity → `generate_video_embedding`.

### `src/activities/upload.py`

- `_perceptual_hashes_match` → `_embeddings_similar` (cosine similarity
  with configurable threshold).
- `_dense_hashes_match` / `_hamming_distance` → deleted.
- Two dedup sites: line ~495 (batch dedup) and line ~616 (S3 dedup) both
  swap to the new matcher.

### `src/data/models.py`

- `perceptual_hash` field becomes `video_embedding` (or both during
  migration — schema documented in `docs/architecture.md`).

### New

- `scripts/backfill_embeddings.py` — backfill script.
- `.env.example` — uncomment the `LLAMA_EMBED_URL` block.

## Out of scope

- Re-architecting the dedup workflow shape. Keep the scoped-by-verification
  pool design (verified vs unverified, parallel `asyncio.gather`); only
  swap the matcher function inside it.
- Cross-event dedup. Today dedup is event-scoped — that stays.
- Re-validating AI clock extraction. The Qwen3-VL prompt logic is
  orthogonal to this change.
