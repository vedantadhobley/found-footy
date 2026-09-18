# Vision retains literal period labels

## Context

Gemma 4 12B sometimes invented normalized `1H`/`2H` values when the image
contained no unambiguous half label. Stronger prompt wording did not enforce
the boundary. Asking for literal label text removed that ambiguity, but the
model-owned temperature default still omitted a real added-time allowance in
one retained natural clip.

Explicit temperature zero plus literal transcription passed the bounded
natural and field-control screen. The pinned OpenAI Go SDK cannot serialize
zero through its scalar `temperature,omitempty` field. Its documented
smallest-positive-float workaround was tested and is not equivalent for this
model: it consistently lost Gueye's `+4` and moved unrelated text into the
period-label field.

## Decision

The model wire contract returns nullable literal `period_label` text. It does
not return a normalized football period. Go maps only an allowlist of explicit
half and extra-time labels to `1H`, `2H`, `ET1`, or `ET2`. Bare numbers,
allowances, clocks, incomplete suffixes, and unknown text provide no period
offset.

`FrameObservation.period` remains readable only for completed Temporal results
and historical evidence. New model responses use a strict decoder that rejects
that retired field, retain the literal label, and evaluate it as
`vision-evaluate-v5`. No historical evidence is rewritten.

Vision requests public `reasoning_effort: none` and explicit
`temperature: 0`. The LLM port exposes temperature as an optional typed value:
nil preserves a model-owned default, while a pointer preserves zero. A
client-scoped HTTP adapter repairs the pinned SDK's zero-omission defect only
for chat-completion requests; it does not change the process-global transport
or any Control model profile. New accepted evidence retains both controls.

## Consequences

The change adds no inference call, image, database migration, gateway setting,
or concurrency. The three-frame request, 60-second deadline, clock tolerance,
content gates, and evaluator policy remain otherwise unchanged.

The exact runtime prompt, schema, adapter, and zero-temperature path passed 39
bounded calls with no transport or schema failure. This is selected-case
qualification, not a general model-accuracy rate. Wrong-match footage can
still pass a matching clock, and graphics-only controls remain unsuitable for
judging the soccer-content gate.

This supersedes the model-owned-sampling choice for Found Footy's vision call
in [Found Footy uses Control's model request contract](./2026-08-25-control-model-request-contract.md).
Other callers still omit sampling unless they establish their own measured
requirement. The [V5 rollout](../history/vision-v5-rollout-2026-09-18.md)
completed without a migration; historical candidate recovery remains a
separate approval.
