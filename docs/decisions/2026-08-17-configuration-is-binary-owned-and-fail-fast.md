# Configuration is binary-owned and fails before external work

## Context

Worker, API, and Twitter all loaded the same `.env`, but the Go loader parsed
the entire top-level configuration for every process. A malformed value for an
unrelated adapter could therefore prevent another binary from starting.
Successful parsing also proved only Go types: zero concurrency, inverted
bounds, an out-of-range discovery attempt, or incompatible Twitter modes could
survive until a later runtime failure. Meanwhile `.env.example`, explicit
Compose overrides, and the variables actually consumed by Go had accumulated
dead and missing keys.

## Decision

- Keep environment selection in Compose. Go receives an opaque binary identity
  and does not branch on dev or prod.
- Give worker, API, and Twitter explicit configuration profiles. `LoadFor`
  derives each profile's variables from config struct tags and parses only
  those sections.
- Validate required values, ranges, paths, URLs, listen addresses, and
  cross-field invariants before a listener, external dependency, or Firefox
  process starts. Aggregate detected semantic violations without including
  configured values or secrets.
- Treat `.env.example` as the canonical operator template. A repository test
  derives the Go variable contract and checks required declarations, stale
  keys, Compose interpolation, explicit service ownership, deployment scope,
  and the atomic cookie-write mount.
- Remove configuration that has no current consumer. Keep constructor checks
  as defense in depth for direct adapter construction.
- Keep the shared `.env` deployment source for now. Per-service secret files
  or a workspace-wide configuration DSL would be a cross-project convention
  and must be decided in `vedanta-dhobley`, not established here implicitly.

## Consequences

A configuration error is a deterministic startup failure at the owning
binary. Unrelated malformed variables do not break another binary, and adding
or removing a typed config field can no longer silently drift from the checked
template and Compose routes.

The private `.env` may retain ignored legacy keys until an explicitly approved
environment edit removes them. This contract limits parsing, not process-level
environment visibility: Compose still injects the shared file into each
application container. Least-privilege secret routing remains a separate
deployment design if the workspace standard adopts it.
