# Video dedup redesign — design proposal (O4/O5)

> **⚠ AS-BUILT DIVERGES — HISTORICAL ONLY (status updated 2026-08-28).**
> This proposal's **topology and schema were largely not built as written.**
> It's kept for the rationale (the cheap→expensive layering, the binary
> category axis, the Python dedup archaeology); do **not** read it as the
> current system. For what shipped, see
> [EventWorkflow ledger](../../../orchestration/event.md) (producer/consumer),
> [`v-phase-orchestration.md`](../../v-phase-orchestration.md),
> [`schema.sql`](../../../../internal/infra/pg/schema.sql), and
> [`decisions.md`](../../../decisions.md).
>
> **Post-release correction:** FF-022 retired the per-candidate
> `VideoWorkflow` child for new histories. `EventWorkflow` now owns the exact-MD5
> claim between download and dense hashing; the child remains registered only
> for replay. See the
> [current orchestration ledger](../../../orchestration/event.md#eventworkflow) and
> [decision record](../../../decisions/2026-08-17-exact-md5-ownership-precedes-dense-hashing.md).
>
> **REJECTED / never built:**
> - **The 3-workflow Discovery→Video→Asset chain.** As-built is a single
>   **EventWorkflow** running a producer (search loop) + a serialized consumer
>   in-workflow, with a **VideoWorkflow child per candidate**. No standalone
>   AssetWorkflow, no signal-with-start, no cross-workflow queue-drain
>   container — completion is `searchDone && inFlight==0` inside EventWorkflow.
> - **Cross-event / per-fixture dedup** (disclaimed 2026-07-25). Dedup is
>   per-EVENT only; `video_assets` is `event_id`-scoped. Cross-event clip-bleed
>   is handled by the vision clock-check, not dedup.
> - **The schema.** No `content_hash` (SHA256), no `perceptual_hash BYTEA`, no
>   `perceptual_hash_prefix` LSH column, no `event_tweets` table. As-built:
>   16-byte **`md5`** exact-match + a per-frame **`frame_hashes`** dHash
>   sequence, `UNIQUE(event_id, md5)`; candidates persist to
>   **`event_search_candidates`**, not `event_tweets`.
> - **Two-checkpoint vision + the LLM "Stage 8" quality-comparison call.**
>   Vision is a single multi-frame `ValidateClip` per surviving clip;
>   winner-selection is **metadata-only** (`video.IsUpgrade`/`ClipQuality` —
>   duration → bits-per-pixel → resolution), wired in EventWorkflow's
>   post-vision dedup path (#171).
> - **`popularity` derived from `COUNT(video_shares)`.** As-built it's a stored
>   `INT` counter bumped `+1` per collapse, with exactly **one share per
>   promoted asset**.
> - **The proposal's ranking inputs.** FF-066 restored read-derived rank, but
>   from the shipped share/asset evidence rather than this proposal's
>   `COUNT(video_shares)`/quality-score model. `video_shares.rank` remains only
>   as a pre-FF-066 Temporal replay field; new histories never rebalance it.
>
> **SURVIVED (the one durable part):** the perceptual-match *algorithm* — dHash
> + histogram equalization + dense frame sampling + offset-tolerant
> sliding-window matching. But the params quoted throughout below are **stale**:
> as-built is **0.1 s** sampling (not 0.25 s) with a tiered match: 27/30 at
> per-frame Hamming ≤12 with 3 gaps, or 45/50 at Hamming ≤16 with 5 gaps. The
> gap-tolerant windows replaced Python's strict `min_consecutive=3`. Source
> of truth: `internal/domain/video/{hash,match}.go` + `config/dedup.go`.

**Status:** historical design proposal — superseded by the as-built (see
banner). It informed the shipped V-phase (EventWorkflow/#164c,
VideoWorkflow/#165) but the shipped shape diverged materially. Retained for
rationale, not as a build spec.

## Topic map

- [`algorithm.md`](./algorithm.md) — original problem statement, semantic
  model, pipeline, dedup layers, and accepted design inputs.
- [`delivery-plan.md`](./delivery-plan.md) — historical sub-commit sequence,
  deferred scope, and walkthrough resolutions.

**Revision log:**
- 2026-07-16 (first pass) — initial proposal, committed 71afc8e.
- 2026-07-16 (second pass) — walked through with user; substantive changes:
  categories collapsed from 3 to binary (wrong-clock = hard-reject, not a stored category);
  metadata hard-filter added as cheapest first-real-work stage (duration/resolution/aspect/framerate);
  content-hash short-circuit reordered ahead of any vision calls so already-owned bytes skip LLM
  entirely; combined vision call for is-soccer + clock check with tighter rubric (fixes Python's
  too-lenient soccer filter); quality-comparison as a second, multi-video vision call for close
  perceptual clusters; AssetWorkflow uses queue-drain completion (counter + queue-empty) instead
  of Python's 5-minute idle-timeout; popularity derived from `COUNT(video_shares)` not a counter;
  perceptual hash at quarter-second frame intervals (revised from 8 uniform-interval guess);
  `event_tweets` extensible table for cross-batch scroll dedup with pluggable per-workflow-type
  processing state.
- 2026-07-16 (third pass, this doc) — grepped Python's actual perceptual hash implementation
  (`archive/src/activities/hashing.py`, `archive/src/utils/dedup_match.py`,
  `archive/src/utils/config.py`) and corrected several algorithm details I had wrong in the
  second pass. Python uses **dHash + histogram equalization + dense 0.25s sampling +
  offset-tolerant sliding-window matching** with `max_hamming=10` per frame and
  `min_consecutive=3` frames as thresholds — NOT pHash + concatenated 512-bit signature + LSH
  as the second pass claimed. The offset-tolerance is load-bearing: same-goal clips with
  different clip boundaries (5s pre-goal vs 2s pre-goal) match via sliding-window offset
  search that signature-Hamming would miss. Q1 (algo choice), Q2 (LSH prefix), and Q3
  (similarity threshold) collapse under Python-preservation. Q4-Q7 unchanged. Storage format
  stays Python's text format initially for cutover compat; binary format is a post-cutover
  optimization. Indexing (LSH or similar) deferred to a future optimization phase — Python
  does all-pairs comparison which is tolerable at our corpus size (~thousands).

**Cross-refs:**
- Plan intent — [`rebuild-plan.md`](../../rebuild-plan.md) §5 W4-W5 (VideoValidation + AssetPersistence)
- Prior decisions — [`decisions.md`](../../../decisions.md):
  - 2026-07-16 Downstream workflow spawn via Temporal-direct + register-on-flip
  - 2026-07-11 Fixture completion contract via pluggable per-event workflow checklist
  - 2026-07-01 Workspace NATS as event bus + Garage / S3 migration
- Upstream — [`discovery.md`](../discovery.md) (Q4 sign-off directs O4 kickoff to open with this doc)
- Python reference — `archive/python/...` (behavior baseline; NOT template)

## Purpose

Replace Python's URL-as-identity dedup — which misses same-clip-
different-URL cases entirely and does no cross-batch dedup against
the S3 corpus — with a layered pipeline that catches identical bytes,
identical content, and near-identical content. Cheap check first at
every layer. Multi-share against existing assets when a match hits at
any layer.

The pipeline lives inside the Video → Asset chain in the downstream
spawn model: Discovery spawns Video (download + validate); Video
spawns Asset (dedup + persist + share). Each stage owns its
`event_downstream_workflows` row per the [2026-07-16 spawn rule](../../../decisions.md).
