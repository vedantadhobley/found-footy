# Vision — soccer/screen validation + clock verification

**Status:** SHIPPED 2026-07-28 (rungs 1–3: `llm` ResponseFormat/DisableThinking
plumb, `internal/domain/vision`, `internal/activity/vision`; see
[`../../decisions.md`](../../decisions.md) "V/4 clip validation"). The
model-dependent bits — previously OPEN — are now **RESOLVED by a real-clip
bake-off** (findings folded in below). **Wired into EventWorkflow's consumer
(#164c)** — `event_pipeline.go` fires it (`fireVision → ValidateClip →
onVisionDone`). Ports the Python clock logic faithfully with a period-awareness
fix; improves the frame/call strategy.

Where it sits: the `Vision` activity is fired **async** per unique clip, *after*
dedup (**only dedup is serial** in the Event consumer's Selector queue; the
vision call runs concurrently and its result rejoins via the Selector — see
[`../v-phase-orchestration.md`](../v-phase-orchestration.md)), so it costs one
joi call per unique clip, not per candidate. joi is the throughput bottleneck
(2 concurrent), so minimizing calls is load-bearing.

## What it decides

Two **complementary** gates + a clock:

1. **soccer** — is this *actual live match footage* (players on pitch, goal,
   replay, celebration, VAR), vs a studio / podcast / press-conference / ads /
   graphics-only clip?
2. **screen** — is someone *filming a TV with a phone* (moiré, bezel, glare,
   tilt, visible room)? vs a clean broadcast/overlay.
3. **clock** — the on-screen match timer, OCR'd and validated against the
   API-reported minute.

**Neither gate is sufficient alone**, and this is the key lesson from prod: a
**commentator/streamer in the crowd** (or a studio desk with a match on a
screen behind them) can have a *valid, correct timestamp* in frame and sail
through the clock check — yet it isn't goal footage. Only the **soccer** gate
rejects it. A timestamp is *necessary-but-not-sufficient*; `soccer` is the
independent "is this the match?" check. (This is exactly the part Qwen was
weakest at — the reason gemma is worth testing.)

## Frame strategy — multi-frame, single call

Python's "smart 2–3 check": frames at **25% / 75%**, and if they *disagree* on
soccer/screen, a **50%** tiebreaker (2–3 separate calls). Its clock was read
from the 25% + 75% frames (the 50% frame was *not* used for the clock).

**Improvement:** pass frames at **25% / 50% / 75% in ONE multi-image call**
(modern VLMs accept several images per message). This:
- **eliminates the tiebreaker round** — the model reasons over all three at once
  and returns **3 per-frame observations** (`{frames: [{soccer, screen, clock,
  added, stoppage_clock} × 3]}`) that `Evaluate` aggregates by 2/3 vote;
- **improves clock recall** — the clock is now read from *three* frames, not two
  (the clock is hidden during replays / graphics / close-ups, so more frames =
  more chances it's visible);
- costs **one joi call** instead of 2–3 — directly relieving the bottleneck.

Frames come from `ffmpeg.ExtractFrame(stagedClip, positionSecs)` (rung-1
adapter) on the cluster's staged clip fetched from Garage.

## The prompt (Python baseline — superseded by schema.go)

> **As-built:** the shipped wire contract is **per-frame** —
> `VisionResponse{ Frames []FrameObservation }` with exactly 3
> `FrameObservation{soccer, screen, clock, added, stoppage_clock}`
> ([`internal/domain/vision/schema.go`](../../../internal/domain/vision/schema.go)),
> driven by the detailed `DefaultPrompt` (not the terse Python one below). The
> single-object JSON here is the historical Python baseline, kept for lineage.

Python's JSON prompt (the baseline the schema grew from):

```json
{"soccer": bool, "screen": bool, "clock": "MM:SS|null", "added": "+N|null", "stoppage_clock": "MM:SS|null"}
```

- `soccer` — live match footage vs studio/ads/graphics.
- `screen` — phone-of-TV only (moiré/bezel/glare/tilt/room); false for pro
  broadcasts/overlays/scoreboards/watermarks/letterbox. "When in doubt, false."
- `clock` / `added` / `stoppage_clock` — the three time fields (below).

## Clock verification — multi-field + OCR-correction + period-aware

Ported from `validate_timestamp` / `compute_absolute_minute`, **with a
correctness fix** (period-awareness).

**Three time fields** can appear together: `clock` (base, e.g. `90:00`) +
`stoppage_clock` (the running sub-timer *within* stoppage, e.g. `02:17`) +
`added` (announced added time, e.g. `+5`). True minute = base + sub-clock
(`90:00 … 02:17` → **92**).

**±1 tolerance:** the API reports the minute *after* the goal, so expected =
`elapsed + extra − 1`. A frame's clock counts as verified if it's within ±1
**and in the expected period**. The Go port briefly clamped normal-time values
back to `elapsed`, silently cancelling the normalization; FF-056 removed that
regression and added explicit expected-minute boundary tests.

**OCR-correction (keep):** in stoppage the model often drops the leading digit
(reads `92:36` as `02:36`). Since `api_elapsed` *is* the dropped base, rebase:
`90 + 2 = 92`. Recovers a whole class of misses.

**Period-awareness (the fix — Python collapses this and gets it wrong).** At
each period boundary the clock restarts (45 / 90 / 105 / 120), so a
*stoppage-of-period-N* value and a *running-into-period-(N+1)* value land on the
**same number but are different halves of the match** — a *different goal*:

| stoppage of period N | running into N+1 | meaning |
|---|---|---|
| 45+3 | 48 | end of H1 vs 3′ into H2 |
| 90+4 | 94 | end of regular time vs ET1 |
| 105+3 | 108 | end of ET1 vs ET2 |

Python's `expected = elapsed + extra − 1` collapses both to one number (45+3 and
48 both → 47), so within ±1 a **wrong-half clip can validate.** Fix: carry
**(period, minute)** on both sides and require *both* to match. The signals are
already present:
- **API:** `(elapsed, extra)` names the period — `elapsed 45, extra 3` = H1
  stoppage; `elapsed 48` = H2; `elapsed 90, extra 4` = regular stoppage;
  `elapsed 94` = ET1.
- **Extracted clock:** the **`+N` added field's presence** = "stoppage of the
  current period"; the frozen base (45/90/105/120) names which period; a bare
  running value with no `+` is the next period.

> **As-built caveat (FF-057):** the shipped parser currently collapses the raw
> clock to an integer before `Evaluate` derives the period. That loses the
> distinction described above for explicit period hints, compact stoppage, and
> bare values at 45/90/105. The ordinary-minute and separate frozen-clock plus
> stoppage-subtimer paths are correct; the boundary representation remains
> deferred work rather than being silently folded into FF-056.

Edge case (only boundary goals), but it produces *confidently-wrong* clips, so
it's worth doing right — and boundary goals are a priority for real-data
validation (synthetic can't cover them).

## Three outcomes → the dedup pools

| outcome | meaning | routing |
|---|---|---|
| **verified** | soccer, not screen, clock present + in ±1 (right period) | → **clock pool** (ranks highest) |
| **unverified** | soccer, not screen, **no clock visible** | → **no-clock pool** (kept, ranks below clock) |
| **rejected** | not-soccer OR screen OR clock present-but-wrong | **dropped** |

This is the clock/no-clock pool split the orchestration wants: a scorebug-cropped
clip (no clock) isn't dropped — it's a lower-ranked tier. And because dHash is
crop-fragile, clock and no-clock versions of the same goal cluster *separately*
anyway (decisions.md 2026-07-28), so **dedup-before-vision holds** (1× joi).

## Resolved — the 2026-07-28 bake-off (real prod clips)

Tested gemma-4-12b (nexus) + Qwen3.5-9B (joi) on real clips: 4 Dybala-match
clips + both variants of two linked prod goals (Lauberbach 90+2, Yeboah 71').
**8/8 correct end-to-end.**

1. **Model behaviour — solved by the config, not the model.** With the detailed
   prompt + thinking-off + json_schema, **both** models score 4/4 on
   soccer/screen (incl. the hard fan-video-of-real-pitch → `screen=false`) and
   read the clock minute-accurately. gemma is the production model (user's
   call); Qwen matched it and was faster (~9s vs ~13s) — **not model-locked.**
   The old "Qwen weak at soccer/screen" note was stale (different model +
   pre-config).
2. **Multi-image single call — works.** Exactly 3 positional frames every time;
   the model reads the clock from whichever frame shows it. No "which image?"
   confusion.
3. **Code-vote chosen** (per-frame observations, we aggregate 2/3 in
   `Evaluate`) — frames are highly consistent, so this is control at no cost.
4. **Prompt is load-bearing.** The terse `screen` definition caused broadcast
   false-positives; the terse time-field wording dropped the frozen-clock
   `+1:48` sub-timer. The detailed [`DefaultPrompt`](../../../internal/domain/vision/schema.go)
   recovers both. **±1 tolerance confirmed** on the real 90+2 (read 90→exp 91)
   and 70:17 (→exp 70) cases.
5. **Thinking-off is a ~3× win** (34s→~13s gemma) with no accuracy loss under
   the schema constraint — `chat_template_kwargs:{enable_thinking:false}`.

The embedding-classifier fallback is **not needed** — soccer/screen are strong.

## Real-prod validation — the screen gate catches what prod misses

For both linked goals, Python-prod surfaces a clean broadcast **and** a
phone-of-TV recording, and the shareable link defaults to the *screen
recording*. gemma flags `screen=true` on both phone-of-TV clips → the gate
rejects them and keeps the clean broadcasts. Strictly better than what's live
at vedanta.systems today.

## Strictness (settled) — see decisions.md for the full rationale

- **±1 minute** (`VISION_TOLERANCE_MINUTES`); not loosened.
- **Period guard strict at halftime, lenient at extra time.** H1/H2 is clean on
  both API + clock sides → hard-reject a wrong-half clip. ET rendering varies →
  a numeric-match-but-period-conflict is soft-kept as `unverified` (not
  dropped). Frozen-boundary stoppage without a sub-timer verifies on period
  alone. The API period map is verified against real WC-2022-final data (all
  boundaries consistent).
