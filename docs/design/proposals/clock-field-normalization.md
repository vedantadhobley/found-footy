# Clock-field normalization — FF-094

## Disposition

Implemented September 15 after explicit user approval, including the bounded
unverified frozen-clock policy, and deployed within v3 in the
[September 17 release](../../history/clock-validation-rollout-2026-09-17.md). The
[decision](../../decisions/2026-09-15-clock-fields-preserve-visible-evidence.md)
and [implementation evidence](../audits/clock-normalization-implementation-2026-09-15.md)
supersede the proposal's approval checkpoints below, retained as design history.
The design followed the [production frequency screen](../audits/clock-field-frequency-2026-09-14.md).
The independent observation fields remain the right contract. Repair their
interpretation in the existing vision domain; do not add an LLM `frozen` guess,
another service, a second model call, or a database migration.

The agreed frozen/no-secondary classification is unverified when the remaining
evidence and the period-end lower bound do not establish a mismatch.

## Current failure and ownership

[`FrameObservation`](../../../internal/domain/vision/schema.go) separates main
clock, visible period, announced allowance and secondary timer. The activity
makes one three-image request and passes the raw fields into the pure evaluator.
The request already instructs the model not to mistake an allowance for a timer.

[`clock.go`](../../../internal/domain/vision/clock.go) nevertheless:

- discards seconds before testing auxiliary fields;
- adds any parsed secondary minutes to the main reading and every alternative;
- discards exact-minute precision for any valid `Added` field without a timer;
- accepts invalid seconds and compact stoppage with non-boundary bases.

[`evaluate.go`](../../../internal/domain/vision/evaluate.go) trusts this
classification. Its period-only frozen fallback was a deliberate
[July 28 decision](../../decisions/archive-through-2026-08-16.md#2026-07-28--v4-clip-validation-vision-llm-soccerscreen-gate--clock-check-rungs-13-shipped),
not an accidental new branch. It must not be silently removed while describing
the entire change as arithmetic repair.

## External evidence

The announced allowance is a minimum additional duration; referees may extend
it. It is neither elapsed stoppage time nor a maximum valid clock value.
Therefore `+5` cannot imply the current minute or invalidate a running `96:xx`
clock. [IFAB Law 7](https://www.theifab.com/laws/latest/the-duration-of-the-match/)
supports this distinction, but does not prescribe a broadcast scorebug format.

The upstream [llama.cpp grammar documentation](https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md)
describes output constraints, a subset of JSON Schema, unsupported conditional
keywords, and the distinction between schema enforcement and prompt content.
This supports retaining interpretation in Go: a syntactically valid `05:00`
can still be assigned to the wrong field. This research did not inspect or
change Control's deployed grammar implementation; upstream capabilities are
not asserted as a live gateway conformance test.

## Recommended design

### 1. Preserve the raw observation contract

Keep `clock`, `period`, `added`, and `stoppage_clock` as nullable independent
observations. Keep the existing soccer/screen fields, three sampling positions,
one model request, model selection and ±1 tolerance. The model transcribes;
Go owns arithmetic. Do not supply the desired event minute as a transcription
hint or infer a visible period label just to make the clip verify.

Agreed transcription constraint (September 14): never manufacture `:00` when
seconds are not visible. A standalone minute-only added-time annotation such as
`+5` or `5′` is allowance evidence, not an elapsed secondary timer. Put a clearly
identified allowance in `added`; leave `stoppage_clock` null when no actual
secondary timer is readable. Preserve uncertainty rather than completing digits.
This rule does not reject the separately supported compact main-clock form
`90+4` or claim every broadcaster uses the same notation.

The `+` sign alone does not identify an allowance, and clock-shaped model output
alone does not prove visible seconds. Reinforce literal transcription in the
prompt and verify it against original frames; deterministic normalization must
still guard against plausible but incorrectly assigned fields. This agreement
does not settle the separate frozen-clock/no-secondary acceptance policy below.

#### Cross-frame context is field identification, not an acceptance gate

Proposed implementation boundary: use the three images already supplied at
25%, 50% and 75% of the clip. The prompt may ask the model to compare scorebug
layout and visible changes to distinguish the main clock, secondary elapsed
timer and announced allowance. It must still report each frame's own visible
characters. No copying missing clocks or period labels between frames,
interpolating digits, inferring elapsed time from sample spacing, or inventing
seconds is allowed.

For example, main readings `95:04`, `95:15`, `95:27` beside repeated `+5′`
support identifying the smaller field as an allowance. Repeated `90:00` beside
`05:04`, `05:15`, `05:27` supports identifying the smaller field as elapsed
stoppage. Unchanging text alone does not establish either role: footage can
contain freeze-frames, cuts and replays.

Do not add a model-generated `frozen`, `continuous`, or progression verdict.
Do not add a Go sequence classifier in this slice. Go continues to normalize
each frame's supplied fields and evaluate the resulting readings together.
No new rule requires increasing timestamps, movement in two or three samples,
all three clocks being legible, or clock deltas matching video sample spacing.
One usable exact minute-and-period match remains sufficient after content
gates; the existing tolerance remains unchanged. This is distinct from the
proposed removal of period-only verification for unpinned clocks below.

Test literal-only prompt wording against wording that explicitly permits this
bounded comparison on the same fixed visual corpus. Comparing raw observations
as well as verdicts must show whether the added context helps field assignment
or contaminates individual readings. Do not treat the shared request itself as
proof that the model uses this context correctly.

### 2. Parse before normalizing

Use a small private parsed-timer value retaining minute, second, and whether
seconds were supplied. A bare `90` is not evidence of `90:00`. Validate complete
supported forms, second range 00–59, and compact boundary bases; preserve the
documented textual-period and compact forms needed by retained observations.
Bad timing fields cannot silently become different valid timing fields.

Keep responsibilities within the existing domain: parse raw display, resolve
supported period/display conventions, then merge compatible auxiliary fields.
Do not add a general clock framework or migrate the existing public DTOs.
Raw observations already retain the seconds durably; a new database column is
not needed to preserve them during calculation.

### 3. Merge fields by what they actually establish

| Observations | Normalization rule |
|---|---|
| Running `95:15`, announced `+5` | Preserve minute 95; allowance is metadata, not elapsed time |
| Running `95:15`, secondary `05:00` | Do not add five; preserve usable main-clock evidence and retain the conflicting raw field |
| Frozen-compatible `90:00`, secondary `05:15` | Minute 95, with second-half stoppage provenance |
| `45:00`, visible `2H`, secondary `01:15` | Use the second-half **end** boundary: 90 + 1 = 91, not 45 + 1 = 46 |
| `15:00`, visible `ET2`, secondary `02:10` | Use the ET2 end boundary: 120 + 2 = 122, not the alternative 105 + 2 |
| Compact `45+2` | Preserve explicit first-half stoppage; never add elapsed stoppage twice |
| Running `90:37`, announced `+4` | Minute 90 remains exact; it cannot verify target minute 93 through an unpinned fallback |
| Bare/malformed/missing main timer | Do not invent seconds or an API-derived main clock from the allowance |

Only a compatible period-ending boundary can participate in additive secondary
clock arithmetic. A syntactically valid secondary field is not sufficient.
A redundant secondary display beside an already cumulative clock may be real;
it still must not be added twice. Invalid auxiliary fields must not erase a
usable main clock or turn a clearly wrong main clock into a soft acceptance.

A single `:00` frame does not prove the display froze. Classify a compatible
boundary with an allowance but no elapsed timer as unpinned evidence under the
proposed policy below, not as proof of a clip-wide frozen display. Other frames
retain their own readings; do not propagate one frame's interpretation across
cuts or require a continuous clock sequence. Preserve the current three-sample
admission policy outside the identified field-consistency changes.

### 4. Keep period ambiguity separate

A bare `95:xx` without an explicit period can still land in the current
ET-versus-second-half-stoppage ambiguity path. Fixing double-counting does not
automatically justify promoting it to verified. Keep that separate from FF-094.
Likewise, the existing leading-digit rebase is a separate accepted heuristic;
the Wöber control's acceptance through it is not proof of correct transcription.

Preserve unassisted reset-clock alternatives such as `45:25 2H` and
`15:10 ET2`. A genuine secondary stoppage timer narrows those alternatives to
the period's end, rather than making both alternatives eligible for addition.

## Accepted policy: frozen main clock without a secondary timer

The v1 code marks `90:00` plus `+5` as verified for **any** second-half
stoppage-time target, including 90+10′. This does not establish its minute.

Agreed September 15: treat a genuinely unpinned clock as **unverified**, provided
the content gates pass and there is no independently decisive contradiction.
Do not pretend minute 90 is its measured event time. Such a sample cannot verify
a clip or neutralize a usable contradictory running clock elsewhere in the
same evaluation. Mixed exact/unpinned observations need explicit tests.

The [September 15 isolated replay](../audits/frozen-clock-policy-2026-09-15.md)
adds an explicit lower-bound constraint: an unpinned timer at a supported period
end still establishes that boundary. If `expected completed minute + tolerance`
is earlier than the period end, it contradicts the target unless another usable
reading independently matches under the existing any-match rule. Do not replace
such a rejection with generic no-clock acceptance. The retained Nico 46′ target
with `90:00 +2` exposes this case. Announced allowance supplies neither the
lower bound nor an upper bound; the compatible period-ending clock supplies it.

The bounded control changed none of 159 natural accepted evaluations, 3,429
rejected rows or 108 separate saved-history observations. No natural acceptance
exercises the proposed downgrade; only 47 currently public shares have linked
natural raw evidence in the snapshot. This limits the no-public-change finding.
It does not validate a new prompt or the complete parser implementation.

This changes a recorded policy. The clip remains eligible, but may move to a
different ranking/dedup category or become hidden by popularity filtering.
Agreement and the new decision are recorded above. Do not retain the
running-clock bypass under the old policy's name.

## Implementation and compatibility surface

- `internal/domain/vision/clock.go` and tests: precision-aware raw parsing,
  supported boundaries, compatible secondary composition and invalid fields.
- `evaluate.go` and tests: consume trustworthy precision; implement the frozen
  agreed no-timer policy. Keep minute normalization and ±1 fixed.
- `schema.go` and tests: reinforce exact transcription with examples of `+5`
  or `5′` versus `05:15`, explicitly forbidding invented `:00`. Do not assume
  more JSON restrictions fix semantic errors;
  test any pattern changes separately against the actual gateway before rollout.
- `evidence.go`: bump `EvaluatorVersion` for new interpretations. Keep envelope
  version 1 and old evidence immutable; `Evidence.Validate` already accepts
  nonempty evaluator versions without re-evaluating historical outcomes.
- Activity output and rejection persistence: retain the evaluator identifier
  for new rejections too; currently only accepted evidence stores it. An
  optional field absent from old recorded outputs must not rewrite their
  candidate payloads. Verify that boundary with workflow regression coverage.
- Docs: update the vision ledger, tests, issue and decision in the same change.

No frontend/NATS change, database migration, new dependency or extra inference
request is needed for the proposed core design. Completed activity results stay
recorded; pending/new activity executions can use the new evaluator. An explicit
rollout approval remains required. Old exact-byte acceptance can still be reused:
deploying this fix is not an implicit revalidation of existing assets or shares.

## Verification plan and research results

Before implementation, the unchanged domain, activity and saved-history audit suites passed on the
pinned Docker toolchain. They include characterization of the bug, so a green
baseline is not proof the proposed behavior exists.

Ten separately labeled constructed probes additionally reproduced:

- `90:37 +4` verifies a 90+4′ target through the unpinned branch;
- `95:15` plus `05:00` becomes minute 100;
- real `90:00` plus `05:15` correctly produces minute 95;
- frozen reset H2/ET2 timers wrongly retain both early-period and stoppage
  alternatives after addition;
- `90:99` and non-boundary compact `70+4` currently parse as usable clocks.

These probes are not production incident counts. They live under ignored
`scratch-audit-2026-09-14/clock_boundary_research.go` and its NDJSON report.

Implementation gates (results and remaining coverage limits are in the linked audit):

1. Add target regressions for the retained Maitland-Niles, Adžić and Fernández
   evidence, plus running/frozen transitions, bare minutes, malformed seconds,
   redundant timers, relative-period boundaries and conflicting explicit labels.
   Preserve acceptance for an exact matching clock repeated across samples,
   a single readable matching clock, and matching readings in reverse order
   through replay edits. These guard against accidentally adding a progression
   or multi-frame agreement requirement.
2. Replay the saved raw observations with clearly separated original and proposed
   results. Do not rewrite historical verdicts or the v1 corpus to make v2 tests
   pass, or retain an unused legacy evaluator in the production path.
3. Confirm genuine frozen-timer, Barcelona reset-half, compact stoppage,
   extra-time, soccer/screen and no-clock cases remain correct. The frequency
   snapshot contains 137 rejected rows with frozen-boundary/secondary shapes,
   so that format must not be dropped wholesale to fix running-clock cases.
4. Test the prompt candidate against a fixed, bounded visual set only after the
   deterministic parser tests pass. Use the same inputs and report failures;
   no retry-until-accepted selection. Include the original Maitland-Niles
   `+5′` allowance and genuine secondary timers with visible seconds; tests
   of prompt wording or supplied JSON alone cannot verify visual transcription.
   Model calls and production repair remain
   distinct from this completed research.
5. Run domain/activity/workflow gates and review old/new evidence serialization.
   Historical repair candidates require a separate media review and approval.
