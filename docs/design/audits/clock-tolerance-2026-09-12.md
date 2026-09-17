# Offline timestamp-tolerance comparison — 2026-09-12 UTC

## Disposition

**Follow-up:** The [broader original-media review](./clock-tolerance-broader-review-2026-09-12.md)
finds useful goal replays at the later edge, a durable-evidence gap and an
independent added-time parser defect. It supersedes the pending broader-review
step below, not these exact saved-history results. Production remains unchanged.

Zero tolerance removes the four adjacent-goal admissions in the saved
Mastantuono histories without changing another outcome in this sample. That
is promising **FF-003** evidence, not approval for a global acceptance change:
the existing, legitimate Abdelkarim lower-edge regression would be rejected,
and multi-goal compilations still pass. **FF-081** replacement suitability is
not solved by this experiment.

The user approved an offline comparison of saved observations. No production
query, configuration change, workflow replay, media fetch, new model request,
deployment, or production data repair occurred. The application evaluator and
its configured default remain unchanged. Code and regression-tool work here
is not a production rollout.

## Method and sample

The [offline command](../../../scripts/audit_clock_tolerance/README.md) calls
the actual `internal/domain/vision.Evaluate` twice on each saved result, with
tolerance one and zero. Expected time comes from that activity's original
`APIElapsed`/`APIExtra`, not the current event row. It retains period handling,
API-minus-one normalization, content gates, and all special clock paths.

Fifteen local history exports contain thirteen distinct event workflows across
five fixtures, observed September 10–11. Two duplicate Mastantuono exports are
counted once. All 108 scheduled vision activities completed and all retain
three raw frames. Every recorded outcome, reason and matched minute reproduces
at tolerance one. There are no missing observations or baseline mismatches in
this selected sample.

The source set is twelve UUID-named exports under
`scratch-audit-2026-09-11/ff092-history/`, both Mastantuono incident histories,
and `scratch-audit-2026-09-11/lens-winner/history.json`. These histories were
saved for incident investigations, **not** a random match-day sample. Counts
refer to completed vision activities, not accepted tweet votes or public clips.
Older human-reviewed regressions are separate controls, not extra history rows.

## Aggregate result

| Current ±1 verdict | Zero-tolerance verdict | Observations |
|---|---|---:|
| Verified | Verified | 35 |
| Verified | Rejected | 4 |
| Unverified | Unverified | 5 |
| Rejected | Rejected | 64 |

Verified observations fall from 39 to 35; accepted observations including the
unverified pool fall from 44 to 40. No event with an accepted observation loses
all accepted observations in this sample. This is **not** a public-set replay:
rejection changes later MD5 claims, deduplication, popularity and placement.

## The four changed verdicts

All are in Venezia–Fiorentina fixture `1550126`:

| Target event | Source tweet ID | Saved clocks | Why this supports stricter admission |
|---|---|---|---|
| Mastantuono 29′ | `2098491069528785033` | 29:36, 29:39, 29:42 | Known restored second-goal source was also admitted into the first event. |
| Mastantuono 29′ | `2098491499054845989` | 29:39, 29:41, 29:43 | Stored text describes a brace and 1–2 score, consistent with the second goal. |
| Mastantuono 30′ | `2098490937458450528` | 28:01, 28:02, 28:04 | Stored text describes the first goal and 1–1 score. |
| Mastantuono 30′ | `2098491102789767598` | 27:59, 28:03, 28:08 | User-identified first-goal keeper; stored text also explicitly names the first goal. |

The two target centers are 28 and 29 respectively. Zero tolerance rejects
each cross-admission; the same known second-goal source still passes for 30′.
Stored text was inspected offline and used as corroboration, not a fresh web
check or a new automated semantic label. The second row's text also incorrectly
says `39′`, illustrating why tweet minute text is not authoritative. No new
visual inspection was performed. See the
[Mastantuono investigation](./mastantuono-bridge-removal-2026-09-11.md) for the
human report, sources, hash evidence and separate repaired placement bug.

## Unchanged cases and hidden differences

All other events keep their recorded outcome counts: Hainaut 86′, Thomasson
52′, Adams 22′, Aouchiche 46′, Wöber 90+13′, Skarke 90+6′, Aguilar 90+3′,
Mastantuono 84′, Gosens 25′, Pellegrino 66′ and Iglesias 81′. Their unchanged
verdicts do not certify semantic correctness.

Four still-verified observations change their compact matched-minute summary:
one first-goal cut changes 27→28; three second-event bridge variants change
28→29. The evaluator returns the first qualifying frame. Narrowing tolerance
skips the earlier neighboring minute, but does not establish which action the
clip opens with or whether every scene belongs to the event.

- The Mastantuono bridge still passes: `28:04, 29:39, 29:42` contains exact
  minute-29 evidence. Three observed encodings retain this pattern.
- The [Lens compilation](./lens-winner-selection-2026-09-11.md) still passes:
  one reading normalizes to 92:44, exactly matching API 90+3′. Requiring the
  exact minute would not prevent its replacement of a focused cut.
- One Wöber observation still verifies through the OCR leading-digit rebase.
  This is the existing evaluator, not a new strict literal-clock parser.
- None of these 108 observations verifies through the frozen-boundary,
  unpinned-minute rule. A separate constructed regression proves that this
  rule remains active at zero tolerance. Changing the integer config alone
  does **not** require an exact minute for every stoppage-time clip.

## Legitimate lower-edge counterexample

The existing FF-057 regression preserves the user-identified Abdelkarim
Barcelona–Al Ahly footage with a `28:56 1H` clock against API 30′. Current
normalization expects 29; tolerance one accepts 28 and zero rejects it.

This audit reuses that recorded user observation as a constructed regression
with the same reading repeated three times. It does not recover the original
three-frame model response or add that constructed row to the 108 histories.
It proves the policy conflict on that input, not a measured new false-negative
rate. A sampled frame can show buildup or a differing provider/broadcast minute;
this evidence does not settle which explanation applies to every such clip.

## Follow-up: require agreement across frames

The user also proposed retaining ±1 but requiring all three timestamps to
match. An offline diagnostic reused the same saved observations and actual
evaluator, considering only the 39 currently verified candidates. Clip-level
soccer/screen votes were unchanged; each frame was evaluated separately with
neutral content flags to isolate its clock support.

| Requirement | Currently verified candidates still qualifying |
|---|---:|
| Current ±1, any qualifying frame | 39 |
| Zero tolerance, any qualifying frame | 35 |
| ±1, three matching clock readings | 31 |
| ±1, at least one matching reading and no contradictory reading | 35 |

The last row is a diagnostic approximation of "all readable clocks match,"
not a settled policy: ambiguous/unpinned readings are unknown rather than
matches; the evaluator's OCR rebase still counts. A bare `90:00` without
stoppage evidence remains a conflict under the existing parser, even though a
human may recognize it as an incompletely read frozen timer.

The three-frame rule fails eight candidates:

- Four have one matching clock and two frames without a visible clock:
  Adams 22′, Aguilar 90+3′, Mastantuono 84′ and Pellegrino 66′.
- Wöber 90+13′ and Skarke 90+6′ have an isolated `90:00` without a sub-timer,
  followed by two matching stoppage readings.
- The known wrong Mastantuono 30′ keeper has `27:59, 28:03, 28:08`; only the
  first sample falls outside the ±1 window. The other three cross-admissions
  removed by zero tolerance still have all three samples inside their target's
  ±1 window and therefore pass this rule.
- Iglesias 81′ has `20:15, 20:15, 80:03`. Those observations alone cannot
  distinguish OCR error from unrelated footage.

All-readable agreement retains the four missing-clock cases and fails the
other four. Neither agreement rule establishes a measured false-positive or
false-negative rate. A failed verification requirement also does not choose
between rejection and demotion to the unverified pool; no such policy changed.
The Mastantuono bridge still has all three readings inside the allowed window.

The local diagnostic is
`scratch-audit-2026-09-12/clock-tolerance/all_frames.go`. It imports the actual
evaluator, consumes the checksum-recorded report, asserts 108 total/39 verified
observations, and ran in a capped, network-disabled Go container. It is not
part of the production application or the permanent audit CLI.

## Decision boundary and next step

Keep production unchanged from this experiment. Zero tolerance is a useful
candidate policy, but four beneficial changed verdicts in an incident-selected
sample do not outweigh the known lower-edge counterexample automatically.

**Agreed direction after the comparison:** prefer a narrower minute window
over mandatory three-frame clock availability. Agreement among usable readings
can be considered separately, but absent clocks and ambiguous frozen timers
must not automatically be treated as contradictory timestamps. The user
recalls the Abdelkarim provider minute being wrong; that explains a possible
tradeoff but was not independently established by this offline experiment.
This records design direction, not a production configuration change or an
accepted implementation of a new fallback rule.

A broader, representative sample should separate exact-minute acceptance,
tolerance-only acceptance, no-clock retention and fallback acceptance, with
human review of the changed candidates. Fresh production exports or media/model
inspection require their own approval. A potential follow-up is targeted extra
sampling for tolerance-only candidates before rejecting them; this has not been
designed or implemented and would add model work. Do not silently adopt it.

Keep FF-081's direct replacement/coverage rule independent. More precise
admission can reduce contamination but cannot make a longer matching clip a
safe substitute for everything it overlaps.

## Reproduction and verification

Local generated output: `scratch-audit-2026-09-12/clock-tolerance/report.json`.
It retains every source SHA256 and the two evaluations with raw frames.
The checked-in, generated
[sanitized history projection](../../../scripts/audit_clock_tolerance/testdata/recorded-histories.json)
preserves the original input/frame/verdict fields, not recomputed baselines,
so the regression remains executable without the full histories. It contains
no media, signed URLs, credentials or unrelated workflow payloads.

The focused tests reproduce the entire historical transition table and
fourteen separately constructed incident/boundary controls. They reject
baseline drift, duplicate-snapshot disagreement, identity errors, missing
inputs and partial frames; repeated identical exports count once. Missing
frames and unfinished activities cannot become invented rejections.

The audit and domain-vision tests passed normally and with `-race -count=3`;
`go vet` passed and pinned golangci-lint reported zero issues for the audit
package. Two full saved-history runs produced byte-identical reports. All
checks ran in memory-capped, network-disabled Docker toolchains. Changed-doc
local link checks and `git diff --check` passed. Full application/integration
gates were not run because no runtime or schema code changed.

```text
report.json              d27621462fdd94100b3f4447b1f17ab4aa271011e704635585ce2cf174f4fdc7
recorded-histories.json   4d030ccdee0d1f99eee73a775f039303dd618a4bfe721c9604ced500b7de35a3
```
