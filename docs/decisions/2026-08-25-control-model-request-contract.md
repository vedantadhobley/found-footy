# Found Footy uses Control's model request contract

## Context

Control now defines typed, model-owned request capabilities and defaults for
the shared Joi and Nexus gateways. The strict gateway rejects runtime-private
fields such as `chat_template_kwargs` and translates the public
`reasoning_effort` field for each pinned backend.

Found Footy's vision adapter sent
`chat_template_kwargs: {"enable_thinking": false}` directly. It also exposed a
temperature-zero setting, but the pinned Go SDK declares temperature as a
non-pointer field with `omitempty`; an explicit zero was absent from the JSON
request. Production therefore used the upstream default rather than the value
claimed by the configuration.

## Decision

The LLM port exposes the normalized optional `ReasoningEffort` field and sends
it as top-level `reasoning_effort`. The vision activity always requests `none`
because its JSON Schema response format requires Gemma's non-reasoning mode.
Backend-private template controls are not part of the application contract.

The unused vision thinking and temperature settings are removed. Vision omits
sampling controls and inherits the selected model profile's Control-owned
defaults. A future project-specific override must use a public field, survive
wire serialization exactly, and pass the gateway's advertised model contract.

## Consequences

Found Footy works with either managed node without knowing its runtime template
syntax. Control can change a backend adapter or a model's evidence-backed
defaults once, while the application retains its explicit structured-output
requirement.

The source migration did not change production by itself. Found Footy's image
release and Control's strict-gateway promotion remained separate authorized
operations with independent rollback.

## Production validation

Found Footy release `e4ae2d7` contains the public request shape. Control then
promoted contract-v3 gateway digest `0fc304bc…` at 15:30 UTC on 2026-08-25.
Its catalog advertises Gemma's `none`/`high` reasoning values, Control-owned
sampling defaults, and the requirement that JSON Schema output use reasoning
`none`.

An exact three-image Found Footy request returned HTTP 200 with valid
three-frame constrained JSON in 7.26 seconds. The gateway rejected the retired
`chat_template_kwargs` field with `unsupported_field` and rejected structured
output paired with reasoning `high` as `incompatible_response_format`. The
application required no restart after the gateway replacement.

## Superseded contract

This supersedes only the backend-private `DisableThinking` mapping and claimed
temperature-zero behavior in the
[2026-07-28 vision decision](../decisions.md#2026-07-28--v4-clip-validation-vision-llm-soccerscreen-gate--clock-check-rungs-13-shipped).
The validated prompt, three-frame request, schema, pinned model, concurrency,
and evaluation rules remain unchanged.
