# Proposal: LLM stack redesign — embedding-augmented vision + concurrency gateway

**Status**: Draft (May 2026). Supersedes the earlier "qwen-embeddings"
proposal, which was scoped only to dedup. The new scope covers:
per-video chat-call reduction, image embeddings for both dedup and frame
classification, a workspace-level LLM gateway for concurrency control,
and joi-side model re-allocation.

> **Update — 2026-05-25 (Path B run, see Track 3 below)**: We ran the
> Path B test on joi with both llama.cpp **b8671** (production) and
> **b9315** (latest). Result: image embeddings do NOT work end-to-end
> via any current llama.cpp build. Root cause is **not** the
> serving runtime — it's that the upstream Qwen3-VL-Embedding-8B model
> is **missing the `1_Pooling` component**. Upstream PR [#18665](https://github.com/ggml-org/llama.cpp/pull/18665)
> was closed 2026-04-22 because of this; issue [#19525](https://github.com/ggml-org/llama.cpp/issues/19525)
> was closed as "not planned". The only working path (sentence-transformers,
> which has its own pooling) is deferred — see "Decisions (locked)" below.

## TL;DR

Three independent tracks. Each has a different blocker so they can ship
on different timelines:

| Track | What | Blocker |
|---|---|---|
| **1. LLM gateway** | Small workspace service in front of joi: global queue, priority, backpressure metrics, per-model concurrency caps | None — implementable today |
| **2. Per-video chat-call reduction (partial)** | Combine 2-3 frame extractions into ONE chat call with multi-image input. Keep clock OCR on chat. | None for the multi-image step — implementable today |
| **3. Image embeddings for dedup + classification** | Replace perceptual hashing AND move soccer/phone-cam classification to embedding similarity. Chat does only OCR. | llama.cpp + Vulkan + Strix Halo doesn't yet support Qwen3-VL-Embedding-8B (vision tower + pooling output). Active discussion: [ggml-org/llama.cpp #19516](https://github.com/ggml-org/llama.cpp/discussions/19516). Three sub-paths below. |

Tracks 1 and 2 are worth doing today regardless of Track 3's outcome.

---

## Current state (May 2026)

### Joi serving stack

llama.cpp on AMD Strix Halo (Framework Desktop, AMD Ryzen AI Max+ 395,
gfx1151) via the **Vulkan backend** (chosen after the ROCm runtime
memory-leak findings — specifically in llama.cpp's HIP interop, not
PyTorch's separate ROCm stack). Three Caddy-fronted slots on joi's own caddy.d/. **Note: these correct earlier proposal claims; verified via direct joi inspection 2026-05-25.**

| Slot | Container | Direct port | Model | Quant | Role today |
|---|---|---|---|---|---|
| `llama-large.joi` | `llama-large` | 3101 | **Qwen3.5-122B-A10B** (MoE, 10B active) | Q4_K_M | Text reasoning + vision (has mmproj). Not actively used by found-footy. |
| `llama-small.joi` | `llama-small` | 3102 | **Qwen3.5-9B** (dense, VL) | Q4_K_M | Vision chat — 5 questions per frame (SOCCER, SCREEN, CLOCK, ADDED, STOPPAGE_CLOCK) plus RAG team-alias selection (text). **This is the model `LLAMA_URL` resolves to.** |
| `llama-embed.joi` | `llama-embed` | 3103 | **Qwen3-Embedding-8B** (text only) | Q4_K_M | Text embedding (4096-dim, last-token pooling). Not actively used by found-footy. **No SigLIP container exists** — earlier proposal drafts mistakenly claimed otherwise based on stale documentation. |

joi's hard cap is **2 concurrent decode streams** (single GPU). The
per-worker `asyncio.Semaphore(2)` in `src/activities/download.py:25` and
`src/activities/rag.py:44` does not enforce this globally — with 8 worker
replicas, up to 32 in-flight requests can pile up; joi processes 2, the
rest queue at the TCP/HTTP layer until a slot frees up.

### found-footy LLM call volume

- **Vision validation** (`validate_video_is_soccer` in `download.py`):
  2-3 chat calls per video × ~5 videos × 10 attempts × N events per fixture.
  A CL night with 50 events ≈ 2500-3700 chat calls.
- **RAG alias selection** (`get_team_aliases` in `rag.py`): 1 chat call
  per team per fixture (pre-cached at daily ingest).
- **Dedup**: pure in-process perceptual hashing (`_generate_perceptual_hash`,
  `_perceptual_hashes_match`, `_dense_hashes_match`). No LLM involvement.

### What's wrong with the current shape

- **Activity timeouts can fire while waiting for joi.** A vision activity
  has `start_to_close_timeout=90s`; if 14 calls queue ahead and each
  takes 8s, the activity expires waiting on a socket.
- **No backpressure visibility.** Operator can't see "joi is 30 deep".
- **No prioritization.** A RAG alias lookup on the hot path of a new
  goal competes equally with the 87th per-video frame validation of an
  older goal.
- **Worker effective throughput is lower than it looks.** Activities
  blocked on joi still count against `max_concurrent_activities=30`.
- **Chat model does work that could be done by embeddings.** Three of
  the five vision questions (SOCCER, SCREEN, and arguably ADDED) are
  classification — embedding similarity is cheaper and parallel-friendly.

---

## Updated serving-stack findings (May 2026)

Re-researched because the previous finding ("vLLM doesn't work well on
gfx1151") was the prior-art that drove our llama.cpp-only stance.

### vLLM on gfx1151
- Not in upstream supported GPU list.
- HIP backend hits driver-level timeout during HIP Graph capture →
  requires `--enforce-eager`, which disables key optimizations.
- Community Fedora-43 docker image exists ([kyuz0/amd-strix-halo-vllm-toolboxes](https://github.com/kyuz0/amd-strix-halo-vllm-toolboxes)) built on TheRock nightly ROCm.
- Vulkan path is documented for Ollama, not vLLM.
- **Verdict**: still poor fit. Community workarounds are degraded.
  ([vLLM #32180](https://github.com/vllm-project/vllm/issues/32180))

### SGLang
- Officially supported on ROCm but targets **Instinct datacenter GPUs**
  (MI300X / MI355X). No Vulkan path. No gfx1151 testing.
- **Verdict**: not viable on our hardware.

### llama.cpp (current production stack)
- Supports Qwen3-VL Instruct models (vision chat with `--mmproj`) ✓ — this is what `llama-small.joi` already runs.
- Supports Qwen3-Embedding text-only models (`--embedding --pooling last`) ✓ — this is what `llama-embed.joi` already runs.
- Does **NOT** yet support Qwen3-VL-Embedding-8B (vision encoder + pooling for embedding output) ✗.
- Community GGUF available: [dam2452/Qwen3-VL-Embedding-8B-GGUF](https://huggingface.co/dam2452/Qwen3-VL-Embedding-8B-GGUF) — may have workarounds; worth a 30-min test.
- Active llama.cpp upstream discussion: [#19516](https://github.com/ggml-org/llama.cpp/discussions/19516). No shipping fix as of late May 2026.
- **Verdict**: production-friendly for chat + text-embed; vision-embed support is the open gap.

### Sentence-transformers / PyTorch
- Works on CPU / CUDA / ROCm / Vulkan but slower than dedicated inference servers (no PagedAttention, limited batching).
- ROCm on Strix Halo had the memory leak that drove us to Vulkan.
- PyTorch Vulkan is limited but improving.
- **Verdict**: workable fallback for the embedding path if llama.cpp doesn't land support; would need to live as a separate service.

---

## Track 1 — LLM gateway

A small workspace-level service that proxies all joi-bound LLM traffic
from found-footy (and eventually other projects) through a single
control plane.

### Why

- Enforces global concurrency limits across all worker replicas (not per-process semaphores).
- Adds priority levels (RAG alias lookup > per-video classification).
- Surfaces backpressure metrics (queue depth, wait time per priority).
- Adds per-model concurrency caps (chat=2, embed=much higher once we measure).
- Lets us swap joi-side endpoints without touching every activity.
- Shareable across long-exposure / spin-cycle / future projects that hit joi.

### Architecture

```
worker activity ──HTTP──> llm-gateway (FastAPI, asyncio)
                              │
                              │ global priority queue
                              │ per-endpoint concurrency caps
                              │ metrics: queue_depth, wait_ms, in_flight
                              ▼
                          joi (via Caddy split-DNS)
                          ├── llama-large.joi
                          ├── llama-small.joi
                          └── llama-embed.joi
```

- **Where it lives**: a new `~/workspace/llm-gateway/` directory with its own docker compose. Joins the `proxy` and `luv-{dev,prod}` shared networks.
- **API shape**: pass-through OpenAI-compatible (`/v1/chat/completions`, `/v1/embeddings`). Add a `X-Priority: high|normal|low` header for queue ordering, default `normal`.
- **Concurrency control**: per-endpoint `asyncio.Semaphore`. Defaults: chat=2 (matches joi cap), embed=8 (embeddings have no decode bottleneck — measure to tune).
- **Queue policy**: FIFO within a priority level; high preempts normal preempts low. Bounded queue (reject with HTTP 503 if depth exceeds N).
- **Timeouts**: configurable per priority; default 60s including queue wait. Client (the worker activity) should configure its own activity-level timeout above this.
- **Metrics**: Prometheus endpoint scraped by the existing monitor stack.
- **Size estimate**: ~300-500 lines of Python (FastAPI + asyncio + httpx + prometheus_client).

### Client-side changes in found-footy

- New env var: `LLM_GATEWAY_URL=http://llm-gateway.<base-domain>` (or cluster-internal hostname on the `luv-{dev,prod}` network).
- Replace direct `httpx.AsyncClient.post(LLAMA_URL/...)` calls with calls to the gateway, passing `X-Priority` per activity type.
- **Delete** the per-process `Semaphore(2)` in `download.py:25` and `rag.py:44`.
- RAG activities tag `X-Priority: high` (one-shot, blocks downstream Twitter search).
- Vision activities tag `X-Priority: normal`.

### Risk

- Single point of failure: if the gateway crashes, all LLM-bearing activities fail until it restarts. Mitigate with `restart: unless-stopped` + healthcheck + (later) hot-standby replica.
- Extra hop adds latency (~1-5ms in-cluster — negligible).

### Effort

S-M: ~1 day. Mostly the queue + metrics code; the API pass-through is small.

---

## Track 2 — Per-video chat-call reduction

Independent of the embedding model. Two sub-steps.

### 2a. Single multi-frame chat call (do now)

Today `validate_video_is_soccer` extracts frames at 25% and 75% of
duration and sends each as a separate chat call (3rd call at 50% on
tiebreaker). Qwen3-VL-8B supports multi-image input. Send all 2-3 frames
in **one** call with a prompt asking the 5 questions across all frames
("for each of frame 1, frame 2, frame 3, answer SOCCER/SCREEN/CLOCK/...").

- Cut: 2-3 chat calls per video → **1**.
- No model change, no joi-side change, no embeddings needed.
- Risk: prompt complexity may degrade structured-JSON adherence — needs validation on the existing `scripts/test_structured_extraction.py` corpus.

### 2b. Move RAG alias selection to llama-large (do now)

`get_team_aliases` currently calls `llama-small.joi` (which today is the
vision chat model — Qwen3-VL-8B-Instruct). The alias-selection task is
pure text and benefits from stronger reasoning. Move to `llama-large.joi`
(Qwen3.5-122B-A10B).

- Frees a `llama-small.joi` slot for vision work.
- Better selections (the 122B model picks more idiomatic aliases than the 8B).
- Risk: `llama-large.joi` is also throughput-capped at 2 concurrent. RAG calls are infrequent enough this shouldn't push it past capacity, but worth measuring after the change.

### 2c. Move SOCCER/SCREEN classification to embeddings (blocked on Track 3)

When Track 3 ships, drop these two questions from the chat prompt; replace with embedding similarity vs a reference set. Chat call then does only CLOCK + ADDED + STOPPAGE_CLOCK (OCR-only).

- Combined with 2a: per-video chat workload goes from 3 calls → 1 call → 1 OCR-only call.

---

## Track 3 — Image embeddings (dedup + classification)

Three sub-paths depending on what works for the Qwen3-VL-Embedding-8B
serving question.

### Path A — Wait for upstream llama.cpp support

- Subscribe to [#19516](https://github.com/ggml-org/llama.cpp/discussions/19516); revisit when fixed.
- Pros: cleanest, no new infrastructure on joi.
- Cons: timeline unknown (weeks to months); blocks the dedup win and the chat-call reduction in 2c.

### Path B — Test the community GGUF on llama.cpp today

- Pull [dam2452/Qwen3-VL-Embedding-8B-GGUF](https://huggingface.co/dam2452/Qwen3-VL-Embedding-8B-GGUF), try serving via `llama-server --embedding --mmproj <mmproj.gguf>`.
- Pros: if it works, fastest path; uses our existing serving stack.
- Cons: may not actually produce correct embeddings even if the server starts (pooling on the vision tower's hidden states is the bit llama.cpp doesn't formally support yet).
- **Action**: 30-minute test on joi. If hidden-state pooling produces embeddings whose cosine similarity matches the reference Sentence-Transformers output (within ε) on a small held-out set, ship.

### Path C — Stand up a parallel sentence-transformers service on joi

- Run a small Python service on joi that uses the official model card's
  Sentence-Transformers code path. Expose `/v1/embeddings` for the gateway.
- PyTorch backend: Vulkan (PyTorch-Vulkan, slower but works) or ROCm
  (risk: the memory leak we hit before). Worth re-testing both on the
  current PyTorch / ROCm releases.
- Pros: unblocks Track 3 today.
- Cons: extra service on joi; throughput likely lower than llama.cpp would be once it gets proper support. We'd want to migrate to llama.cpp later when support lands.

### Recommended approach for Track 3

Sequence: **Path B (30 min) → Path C if B fails (~half day) → Path A as the long-term destination**.

### 2026-05-25 — Path B run, result

- Pulled `dam2452/Qwen3-VL-Embedding-8B-Q8_0.gguf` (8.05 GB) and `Qwen/Qwen3-VL-8B-Instruct-GGUF/mmproj-Qwen3VL-8B-Instruct-F16.gguf` (1.16 GB) to `~/.gguf/` on joi.
- Swapped `llama-embed` to load both files with `--embedding --pooling last`. Container started cleanly, mmproj loaded successfully, `/embedding` accepted both `content+image_data` and `[img-N]`-marker payloads.
- Tested on llama.cpp **b8671** (production) and built a parallel `llama-server:vulkan-b9315` image to also test against **b9315** (latest).
- Both builds: same failure mode. Two visually-distinct test images produced **identical embeddings (cosine 1.0)** with empty `content`. With prompt markers (`[img-10]`, Qwen vision tokens), embeddings differed by ~0.0001 (cosine 0.999) — i.e. dominated by text-token positions, not image content.
- **Verdict**: image data is not flowing through the vision tower into the pooled embedding output on either build. This is the failure pattern in [#19525](https://github.com/ggml-org/llama.cpp/issues/19525) (closed "not planned"). PR [#18665](https://github.com/ggml-org/llama.cpp/pull/18665) (which would have fixed this) was closed 2026-04-22 with the maintainer noting the upstream Qwen weights are **missing the `1_Pooling` component**, making the model "not actually ready to be used unless Qwen team fixed it".
- Joi was reverted to `Qwen3-Embedding-8B-Q4_K_M` (text-only) the same day. All three endpoints (`llama-large.joi`, `llama-small.joi`, `llama-embed.joi`) confirmed functional.
- The new GGUF + mmproj are kept on disk (~9 GB total) in case the upstream issue resolves later. `llama-server:vulkan-b9315` image kept in the docker registry, available for a future upgrade.

### Once embeddings work — what changes in found-footy

#### Vision classification (replaces 2-3 chat calls)
- Build reference embedding sets at startup:
  - `soccer_broadcast_embeddings`: ~50 manually-labelled broadcast frames
  - `phone_cam_embeddings`: ~50 manually-labelled phone-filming-TV frames
  - `not_soccer_embeddings`: ~50 manually-labelled negative frames
- Per video: embed the extracted frames, compute cosine similarity to each reference set, classify by nearest centroid (or threshold).
- Embedding latency target: <500ms per frame on joi. (TBD pending Path B/C testing.)

#### Video dedup (the original proposal)
- `_generate_perceptual_hash` → `_generate_video_embedding`:
  - Sample 3-5 keyframes, embed each, store as ordered sequence (or pooled mean).
  - MRL: store at 256-dim for storage efficiency; use full 4096-dim only for comparison if 256-dim is ambiguous.
- `_perceptual_hashes_match` / `_dense_hashes_match` / `_hamming_distance` → delete.
- New `_embeddings_similar(a, b, threshold)` using cosine.
- Threshold tuning: use the existing S3 corpus as a labeled set — every clip the old hash system clustered as a duplicate is a positive example.
- Storage: embeddings inline in `_s3_videos[*].video_embedding` field in MongoDB. Schema doc lives in `docs/architecture.md`.

#### Migration
- One-shot backfill script: `scripts/backfill_embeddings.py`. Streams every video in MongoDB through the embedding endpoint, writes vector to the document. ~hours, run overnight.
- Keep both `perceptual_hash` and `video_embedding` fields during transition; switch matchers in a single commit; drop hash field in a follow-up after a week.

---

## Proposed joi slot assignment

Final state after all three tracks ship:

| Slot | Model | Role |
|---|---|---|
| `llama-large.joi` | Qwen3.5-122B-A10B (unchanged) | Text reasoning + RAG alias selection (moved from `llama-small`) |
| `llama-small.joi` | Qwen3-VL-8B-Instruct (unchanged, but narrower scope) | Vision chat for OCR only (CLOCK + ADDED + STOPPAGE_CLOCK). User noted this model is working well; not changing the chosen model. Open to revisiting once we measure post-redesign load (could a smaller VL chat model handle the narrowed OCR-only scope?). |
| `llama-embed.joi` | **Qwen3-VL-Embedding-8B** (new) | Image + text embeddings for dedup and frame classification. Today's `Qwen3-Embedding-8B` (text-only) gets retired or moved if anyone else uses it. |

---

## Migration sequencing

Suggested ordering — independent enough that they can run as separate sessions:

| Step | Track | Blocked by |
|---|---|---|
| 1 | **2a** — combine frame extractions into single multi-image chat call | nothing |
| 2 | **2b** — move RAG alias selection to llama-large | nothing |
| 3 | **1** — build LLM gateway, route all calls through it | nothing |
| 4 | **3 Path B test** — try community GGUF for Qwen3-VL-Embedding-8B on llama.cpp | nothing (30 min test) |
| 5 | If 4 works: **3 dedup migration** — embeddings replace perceptual hashes | step 4 |
| 6 | After 5 settles: **2c + 3 classification** — embeddings replace SOCCER/SCREEN chat questions | step 5 |
| 7 | If 4 fails: **3 Path C** — stand up sentence-transformers service on joi, then steps 5-6 | step 4 failing |
| 8 | (much later) When llama.cpp lands proper support: migrate Path C → llama.cpp | upstream support |

Steps 1-3 are unblocked-today wins. Step 4 is a one-shot experiment with
high information value. Steps 5-6 deliver the dedup robustness + chat-call
reduction the user cares about.

---

## Out of scope

- **Smaller VL chat model exploration**. User noted the current Qwen3-VL-8B
  works well for OCR. Deferred — revisit once Track 3 ships and we can
  measure whether 8B is overkill for the narrowed OCR-only workload.
- **Cross-project adoption of the LLM gateway**. Designed to be shareable
  but other projects (long-exposure, spin-cycle) are not customers in v1.
- **Re-architecting the dedup workflow shape**. Keep scoped-by-verification
  pools (verified vs unverified, parallel `asyncio.gather`); only swap
  the matcher function inside it.
- **Cross-event dedup**. Stays event-scoped.
- **Re-validating AI clock extraction.** Qwen3-VL-8B's OCR for the clock
  fields is working; the structured prompt validation isn't changing in
  this proposal.

---

## Decisions (locked May 2026)

1. **Gateway location**: standalone stack at `~/workspace/llm-gateway/`. Joins `proxy` + `luv-{dev,prod}` networks. Cleanest separation; shareable with other projects later.
2. **Gateway hostname**: `llm.<base-domain>` (no project prefix — it's shared infra).
3. **~~Path B test timing: deferred~~** — **RAN 2026-05-25**. Result documented in Track 3. Test confirmed the upstream model bug; no further llama.cpp investigation worth pursuing until Qwen republishes the model or a community fork ships a working alternative.
4. **Sprint ordering relative to cleanup work**: cleanup first. Sequence in `docs/sprints.md`: Sprint 1 (Lazio Pisa + NameErrors) → Sprint 2 (Mongo atomicity) → Sprint 3 (dead code + deps) → LLM Track 1 (gateway) → LLM Track 2 (chat-call reduction) → Track 3 deferred indefinitely (Path A waiting on Qwen, Path C requires standing up sentence-transformers stack on joi which is non-trivial — revisit when image-embedding becomes a higher priority).
5. **Path C deferred (2026-05-25)**: We started the sentence-transformers POC on joi (model downloads cached at `~/.cache/huggingface/.../Qwen3-VL-Embedding-8B/`, ~16 GB) but did not stand up a service. Backend choice (CPU vs PyTorch ROCm vs PyTorch Vulkan) is unresolved — PyTorch ROCm is the most promising option, was previously avoided based on a memory-leak finding that turned out to be specific to llama.cpp's HIP runtime (a different code path from PyTorch's). When/if image-embedding work resumes, start there.

## References

- [Qwen3-VL-Embedding-8B model card](https://huggingface.co/Qwen/Qwen3-VL-Embedding-8B) — 4096-dim, MRL-tunable to 64-4096, vision+text, Apache 2.0, 77.9 MMEB-V2
- [Community GGUF: dam2452/Qwen3-VL-Embedding-8B-GGUF](https://huggingface.co/dam2452/Qwen3-VL-Embedding-8B-GGUF)
- [llama.cpp discussion #19516 — Qwen3-VL-Embedding support](https://github.com/ggml-org/llama.cpp/discussions/19516)
- [vLLM issue #32180 — gfx1151 instability](https://github.com/vllm-project/vllm/issues/32180)
- [kyuz0/amd-strix-halo-vllm-toolboxes](https://github.com/kyuz0/amd-strix-halo-vllm-toolboxes) — community Strix Halo vLLM workaround
- [SGLang AMD docs](https://docs.sglang.io/platforms/amd_gpu.html) — Instinct datacenter GPU only
- [llama.cpp multimodal docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/multimodal.md)
