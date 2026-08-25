# Found Footy uses Control's canonical Joi gateway

## Context

Found Footy production used `http://joi.luv`, the legacy gateway identity that
predated Control's ownership of managed inference deployments. Control now
publishes and runs an immutable Joi gateway at `http://control-joi.luv` while
retaining the old route as an independent rollback surface.

Keeping the application on the old identity after acceptance would preserve
deployment-authority drift: Control would own the release, but the production
consumer would still name the predecessor control plane.

## Decision

Found Footy production sets `LLM_ENDPOINT_URL=http://control-joi.luv` and keeps
`LLM_CHAT_MODEL=gemma-4-12b`. The cutover changes neither client semantics nor
capacity: the two worker processes retain their existing per-process admission
cap and run the same immutable application release.

Only the workers load this setting. The production cutover therefore recreates
only those replicas. Acceptance requires their exact release identity, clean
dependency startup, initialized LLM route, process health, scheduled work, the
gateway's ready model catalog, and a bounded exact Gemma response.

## Consequences

The private production `.env` remains the live configuration, and
`.env.example` records the canonical identity. Found Footy does not own or
deploy either gateway. Control may retire `joi.luv` only after its separate
rollback and observed-use gates pass.

## Superseded contract

This supersedes only the `joi.luv` endpoint identity in the
[2026-08-11 stopgap decision](../decisions.md#2026-08-11--llm-path-joiluv-gateway--gemma-pin-concurrency-cap-24-stopgap).
The pinned model and process-local concurrency contract remain in force.
