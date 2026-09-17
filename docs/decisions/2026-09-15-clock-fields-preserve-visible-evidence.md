# Clock fields preserve visible evidence

## Decision

FF-094 uses `vision-evaluate-v2`. The model transcribes the independent main
clock, period, allowance and elapsed secondary timer. Go owns normalization.
The later [visible-period decision](./2026-09-17-relative-clocks-require-visible-period.md)
builds on this normalization as v3, removing only unlabelled relative soft-keep.
The user approved implementation on September 15 after the
[isolated frozen-clock replay](../design/audits/frozen-clock-policy-2026-09-15.md).

- Preserve visible seconds until auxiliary composition. Minute-only input is
  not evidence of `:00`. Reject incomplete timer syntax, invalid seconds and
  compact stoppage with a non-boundary base.
- An allowance is not elapsed time or a ceiling. It cannot erase a running
  main timer. A secondary timer adds only to a compatible, seconds-visible
  zero-second period-ending boundary. Compact clocks are already composed.
- Secondary arithmetic resolves reset/continuous collisions to the end of
  the observed period: `45:00 2H +01:15` means minute 91, not 46;
  `15:00 ET2 +02:10` means 122, not 107.
- A compatible boundary with an allowance but no elapsed timer is unpinned.
  It cannot verify on period alone. Keep it unverified only when remaining
  evidence does not establish a mismatch. Preserve its period-end lower bound:
  a target window wholly before that boundary remains incompatible.
- Any independently usable exact match can still verify the clip. Missing,
  repeated or reversed samples do not create a new progression/agreement gate.
  Existing period ambiguity and OCR leading-digit rebasing remain separate.

This supersedes the period-only frozen-clock verification in the
[July 28 decision](./archive-through-2026-08-16.md#2026-07-28--v4-clip-validation-vision-llm-soccerscreen-gate--clock-check-rungs-13-shipped).
It does not change `elapsed + extra - 1`, ±1 tolerance, sampling positions,
content votes or model choice.

## Transcription and failure boundaries

The prompt forbids invented seconds. The three existing images may help
identify field roles, but missing digits/periods must not be copied or inferred
across frames. There is no new model-generated progression/frozen verdict,
extra inference call, model-output field, dependency or database column.

Unparseable main clocks supply no usable minute; they do not become guessed
valid clocks. With no other usable timing evidence, passing soccer footage
still enters the existing unverified pool. The historical replay identifies
six newly unverified rows with malformed main fields; this is not proof those
videos belong to the event. Invalid auxiliary fields never destroy a usable
main clock. Raw observations remain available for diagnosis.

## Durability and rollout

Accepted evidence retains envelope version 1 and identifies the evaluator as
v2. New activity results also carry an optional evaluator identifier into
rejected candidate detail. Old completed results omit it and must produce the
same historical commands; no current version is fabricated during replay.

Old accepted proof is not re-evaluated when loaded. Existing exact-MD5 reuse
can therefore still reuse a v1 acceptance after deployment. Historical repair
requires separate review and approval; a release is not an automatic cleanup.
Category changes can affect dedup, support, ranking and singleton visibility.

The [implementation replay](../design/audits/clock-normalization-implementation-2026-09-15.md)
keeps recorded and current verdicts separate. This normalization shipped within
v3 in the [September 17 release](../history/clock-validation-rollout-2026-09-17.md);
natural-event validation remains open.
