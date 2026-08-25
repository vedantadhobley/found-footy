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

The source migration does not change production by itself. A Found Footy image
release and worker-only rollout remain separate authorized operations. The new
strict gateway must accept the unchanged JSON Schema request before promotion.

## Superseded contract

This supersedes only the backend-private `DisableThinking` mapping and claimed
temperature-zero behavior in the
[2026-07-28 vision decision](../decisions.md#2026-07-28--v4-clip-validation-vision-llm-soccerscreen-gate--clock-check-rungs-13-shipped).
The validated prompt, three-frame request, schema, pinned model, concurrency,
and evaluation rules remain unchanged.
