# Vision validation — Python behavior spec

Frozen WHAT-and-WHY detail from the
[Python functional-spec index](./README.md).

## Vision / AI validation — Python behavior spec (WHAT + WHY)

File referenced: `archive/src/activities/vision.py`.

### 1. `validate_video_is_soccer` as the single combined call

PURPOSE: One vision LLM invocation per frame decides "is this soccer, is this a phone-of-TV recording, and what does the clock show" together, so each frame is billed once for all three concerns (lines 551-927).

BEHAVIOR:
- A single `_call_vision_model` call per frame returns one JSON object satisfying all three questions (lines 623-636, 742, 759).
- The activity is the sole entry point for validation (`@activity.defn`, line 551), so downstream code sees one Temporal activity, not three.
- Image tokens are loaded once per frame decision; splitting into three calls would triple that cost against joi's throughput budget.
- Structured output is enforced via `response_format: json_object` (line 462), giving the parser (lines 643-714) a deterministic contract.
- Retries, semaphore admission (line 52), timeouts, and typed errors apply per frame — not per subquestion — halving the failure surface.
- Return payload merges all three concerns (lines 914-927): `is_valid`, `is_soccer`, `is_screen_recording`, `clock_verified`, `extracted_minute`, `timestamp_status`, `extracted_clocks`.

REMARKS: The Phase 5 note (lines 24-27) foreshadows splitting the internals — soccer/screen may migrate to embeddings while OCR stays on the chat VL model — but the "one round-trip per frame" external contract is what callers depend on. The rebuild's Phase O4 should preserve that shape even if internals split.

### 2. Dual checkpoint at 25% and 75% frame positions

PURPOSE: Two frames sampled a half-video apart give redundancy against transient bad frames — a graphics-only cut, a black transition, a crowd close-up without the clock visible.

BEHAVIOR:
- Frame timestamps are `duration * 0.25` and `duration * 0.75` (lines 717-718), straddling the midpoint at any length.
- Each frame is fed to the same prompt in a separate call (lines 742, 759) and parsed independently.
- Agreement on both `soccer` and `screen` yields a fast answer at confidence 0.90-0.95 (lines 809-812, 830-831).
- Disagreement on either dimension triggers a 50% tiebreaker with 2/3 majority voting (lines 774-798, 815-819, 833-836).
- If one of the two frames fails extraction, single-frame fallback runs at confidence 0.7 (lines 801-808, 826-829); if both fail, the activity raises `RuntimeError` (lines 723-727).
- Each check emits a Temporal heartbeat so long videos don't time out (lines 741, 755, 783).

REMARKS: Cost is 2 LLM calls per video in the common case, 3 with tiebreaker. Only 25%/75% frames feed timestamp validation (line 794: "Extracted but NOT used for timestamp validation") — the tiebreaker exists only to resolve soccer/screen disagreement. The rebuild must preserve this; adding the 50% clock to the verification pool would silently loosen acceptance.

### 3. The 5-field JSON output shape

PURPOSE: The model returns exactly `{soccer, screen, clock, added, stoppage_clock}` (line 626) so the parser has an enumerable schema with clear null semantics.

BEHAVIOR:
- `soccer`: boolean; true means "soccer broadcast content of any kind" per the rubric (line 628).
- `screen`: boolean; true means "phone filming a TV" (line 630). Bias-toward-false when uncertain.
- `clock`: string like `"34:12"` or `"90:00"`, or `null` if no primary timer visible (line 632).
- `added`: string like `"+4"`, or `null` if no added-time indicator (line 634).
- `stoppage_clock`: string like `"03:57"`, or `null` if no separate sub-timer (line 636).
- Parser reads JSON directly (lines 667-674); a regex text-fallback path (lines 678-713) recovers responses from models that ignored `json_object`.
- Null on any clock field means "not visible in this frame" — distinct from "visible but wrong" — and drives the "unverified" branch below.

REMARKS: The 5-field shape is the contract downstream expects. `parse_response` normalizes to `is_soccer`, `is_screen`, `raw_clock`, `raw_added`, `raw_stoppage_clock` (lines 645-646) preserving null through to `validate_timestamp`. The rebuild's typed struct should mirror this exactly.

### 4. `soccer` rubric today

PURPOSE: True for any soccer broadcast footage — live match, replay, celebration, VAR, stadium recording (line 628).

BEHAVIOR:
- In scope per prompt (line 628): "players on pitch, match action, goals, replays, celebrations, VAR footage, stadium recordings."
- Out of scope per prompt (line 628): "studio/podcast, press conference, ads, other sports, or just graphics with no match footage."
- Text-fallback also accepts "SKIP" as soccer-true (lines 682-684), inherited from an older prompt.
- Classification is per frame — a promo insert at one frame won't reject the video if the other frame is clean.

REMARKS: **The user has flagged this as too lenient for production.** Including "celebrations" and "stadium recordings" unconditionally is why non-broadcast content (fan-shot phone videos of trophy lifts, stadium exteriors, tunnel walks) still passes. The rebuild's `docs/design/proposals/video-dedup/README.md` rubric should tighten this: celebrations should require in-play context (players in kit on pitch, immediate goal aftermath), stadium recordings should require active match play visible. Splitting `soccer` into `soccer_broadcast` vs `soccer_adjacent` would let ranking keep celebrations without letting them count as passing broadcast content.

### 5. `screen` rubric today

PURPOSE: True when a physical camera is filming a TV set (line 630): moiré, bezel, glare, tilt, room visible.

BEHAVIOR:
- Positive cues per prompt (line 630): "moiré patterns, visible TV bezel, screen glare, tilted angle, visible room/furniture."
- Explicit false-positives to reject (line 630): "professional broadcasts, overlays, scoreboards, watermarks, letterbox bars."
- Default-false posture: "When in doubt, false" (line 630) — bias toward keeping.
- Text-fallback fires on keywords `MOIRE`, `BEZEL`, `TV FRAME` (line 693).
- 2/3 majority to REJECT (line 836): asymmetric — you need at least two votes for screen-true.

REMARKS: **This does NOT catch software screen recording** (OBS-style browser capture, capture-card DVR, in-browser player recording). Those clips lack moiré and bezel because the signal is captured digitally, but they're still re-uploads of someone else's stream. This is a known rebuild-time gap; the rebuild's tightened rubric needs a separate signal for it (streaming-service watermarks, browser chrome, DVR progress bars) or must rely on S3-corpus dedup as the sole defense.

### 6. Clock extraction — three fields, one truth

PURPOSE: Broadcasts show *two* clocks during stoppage — a frozen main clock at 45:00 or 90:00 plus a smaller counting-up sub-clock — and both are needed to reconstruct absolute match minute.

BEHAVIOR:
- `clock` captures the primary timer (line 632). Parsed by `parse_clock_field` (lines 198-244) which handles running "34:12", period indicators "2H 15:30" / "ET 04:04", and compact stoppage "45+2".
- `added` captures the "+N" indicator (line 634). Parsed by `parse_added_field` (lines 247-260) — currently informational, not summed into absolute minute.
- `stoppage_clock` captures the sub-timer minute component (line 636). Parsed by `parse_stoppage_clock_field` (lines 263-279).
- `compute_absolute_minute` (lines 282-300) sums `clock + stoppage_clock` when both present: "90:00" main + "02:17" sub → minute 92.
- Smart offset in `parse_clock_field` (lines 238-244) disambiguates "2H 15:30" as 60 (relative) vs "2H 67:00" as 67 (absolute) by numeric magnitude.
- Sentinels "NONE", "HT", "FT", "HALF TIME", "FULL TIME" map to `None` (line 212).

REMARKS: `added` is captured but only `stoppage_clock` is summed — `added` is *allocated* stoppage time (bounds), `stoppage_clock` is *elapsed*. The rebuild should preserve both fields even though the current summation ignores `added`; it becomes load-bearing when OCR quality improves and we can trust "+N" as a range check.

### 7. Timestamp validation

PURPOSE: Compare each frame's extracted minute to the event's API-reported minute + extra, with ±1 tolerance, and classify verified / unverified / rejected (lines 303-369).

BEHAVIOR:
- Expected minute = `api_elapsed + (api_extra or 0) - 1` (line 337); the `-1` accounts for API reporting the minute *after* the goal.
- Direct match: any parsed frame minute within ±1 → `"verified"` (lines 351-354).
- OCR-correction phase: if the model dropped a leading digit ("92:36" read as "02:36"), rebase by adding `api_elapsed` and re-check ±1 (lines 361-365).
- No clock visible in any frame → `(False, None, "unverified")` (lines 348-349).
- No `api_elapsed` supplied (e.g., in-flight replay with default=0) → `(False, None, "unverified")` (lines 333-334).
- Clock visible in ≥1 frame but no phase matches → `"rejected"` with the closest minute returned for logging (lines 367-369).
- Only 25%/75% frames are fed in (lines 844-849); the 50% tiebreaker is deliberately excluded (line 794).

REMARKS: The three-state classification is the load-bearing output of this function — see §8.

### 8. `is_valid` derivation — REJECTED vs UNVERIFIED

PURPOSE: `is_valid = is_soccer AND NOT is_screen_recording AND timestamp_status != "rejected"` (lines 841-864).

BEHAVIOR:
- Baseline: `is_valid = is_soccer and not is_screen_recording` (line 842).
- If `timestamp_status == "rejected"`: `is_valid` is forced False (lines 862-863) — the video is **discarded**.
- If `timestamp_status == "unverified"` (no legible clock): `is_valid` stays True — the video is **kept** in the corpus.
- If `timestamp_status == "verified"`: `is_valid` stays True and downstream ranking gets a positive `clock_verified=True` signal.
- All three fields are returned in the payload (lines 923-925) so callers can rank kept-and-verified above kept-but-unverified.

REMARKS: **This is the load-bearing distinction the rebuild must preserve.** "Rejected" means the clock said the wrong minute — evidence of the wrong goal or wrong match — and discarding is safe. "Unverified" means no clock was legible — the clip might be a valid celebration or replay whose visible clock got covered by an overlay — and keeping it at lower rank protects recall. Collapsing these into a single "not verified → drop" would silently gut the corpus of legitimate no-clock footage. The video-dedup proposal's tightened rubric should keep this three-state output as the timestamp contract.

### 9. Handling low-confidence responses, JSON parse failures, LLM timeouts

PURPOSE: The LLM path is unreliable in three flavors — HTTP failure, parse failure, and semantic uncertainty — each with a distinct behavior.

BEHAVIOR:
- Semaphore-gated concurrency: `_LLM_SEMAPHORE = asyncio.Semaphore(LLM_CONCURRENCY_PER_WORKER)` (line 52), pinned to 2 per worker to match joi's `--parallel 4` / 2-worker configuration.
- Retries up to 3 times on `httpx.TimeoutException` and `httpx.ReadError` with linear backoff (`3 * attempt` seconds, lines 511-517).
- 503 from joi is logged distinctly as `vision_cap_exceeded` (lines 482-487) so parallel-cap contention is diagnosable.
- Connect failures raise typed `LLMUnavailableError` (lines 501-508); exhausted retries raise `LLMTimeoutError` (lines 518-529); unexpected exceptions raise `LLMValidationError` (lines 530-541).
- Non-200 non-503 responses log `vision_http_error` and return None after retries (lines 488-500).
- Parse failure path: `parse_response` catches `json.JSONDecodeError` and `TypeError` (line 675), falls back to regex text parsing (lines 678-713); if that finds nothing, returns all-false / all-null.
- No confidence field from the model — confidence in the return payload (lines 811, 818, 822) is derived from voting agreement, not the LLM.
- Video too short (<1s duration) short-circuits to `is_valid=True, confidence=0.5` (lines 611-619).

REMARKS: Graceful degradation is deliberate — LLM outages should not force-drop videos. But it also means a hallucinating model would happily pass everything at confidence 0.9. The rebuild should consider an explicit "LLM produced no usable signal" state distinct from "LLM said not-soccer."

### 10. Frame extraction subroutine

PURPOSE: `_extract_frame_for_vision` pulls a single PNG frame at a target timestamp via ffmpeg and returns it base64-encoded for the LLM's `image_url` field (lines 377-422).

BEHAVIOR:
- Command: `ffmpeg -ss <ts> -i <file> -vframes 1 -f image2pipe -vcodec png -` piped to stdout (lines 392-400) — no on-disk intermediate.
- 10-second subprocess timeout (line 406) with distinct log actions for `frame_extraction_failed`, `frame_extraction_timeout`, `frame_extraction_error` (lines 409, 416, 420).
- Duration is probed once via `ffprobe format=duration` before frame extraction (lines 598-605); failure defaults to 10.0s (line 609).
- 25%/75% timestamps are always `duration * 0.25` / `duration * 0.75` — no length-specific branch beyond the <1s bail (line 611).
- If EITHER frame extracts, validation proceeds (lines 723-727); only if BOTH fail does the activity raise `RuntimeError`.
- Returned base64 is embedded as `data:image/jpeg;base64,...` despite being PNG (line 453) — llama.cpp tolerates the content-type mismatch.

REMARKS: The <1s bail is the only short-video branch — a 2s clip still gets 25%/75% sampling (frames at 0.5s and 1.5s). The rebuild should consider an explicit minimum-spacing rule so both frames aren't effectively the same shot.

### 11. Historical notes visible in the file

PURPOSE: The docstring and inline comments carry three forward-looking pieces of context.

BEHAVIOR:
- **Phase 3 module split** (P3a, 2026-05-26) — vision was extracted from `download.py`; `MODULE = "download"` (line 48) is deliberately kept so Grafana dashboards and the Phase 1 query catalog keep working (lines 14-18). Log-identity vs code-organization are documented as intentionally separate.
- **Phase 5 planned replacement** (lines 24-27) — soccer/screen classification may migrate to Qwen3-VL embedding-based classification, leaving only OCR (clock/added/stoppage_clock) on the chat VL model. The clock parsers survive that migration.
- **Legacy `parse_broadcast_clock`** (lines 60-190) is retained only for the `test_clock_parsing.py` harness (lines 96-99); production uses the structured field parsers.
- **Text-fallback parsing** (lines 678-713) is a backstop from before `response_format: json_object` was reliable; kept for older models.
- **`LLM_CONCURRENCY_PER_WORKER = 2`** (line 52) is pinned to joi's `--parallel 4` / 2-worker config; changing joi's concurrency requires a paired change here.

REMARKS: Phase 5's soccer/screen → embeddings direction aligns with the rebuild's video-dedup proposal — keep OCR on the chat model, move classification to embeddings. The `MODULE = "download"` continuity discipline should be re-decided in the rebuild's `vocabulary` registry rather than silently inherited; it is a Python-era log-schema constraint that Grafana queries lock in place, and the Go rewrite has an opportunity to name the module correctly at the source.

---
