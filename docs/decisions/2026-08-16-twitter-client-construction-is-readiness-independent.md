# Twitter client construction is independent of browser readiness

## Context

The worker constructed its Twitter HTTP client by probing the shared browser's
`/health` endpoint. A failed probe was treated as non-fatal so fixture polling
could continue, but discovery was then registered with a nil client for the
worker's entire lifetime.

This is especially unsafe during an application release: workers and Twitter
are recreated together, the client probe allowed ten seconds, and initial
browser authentication can take up to 45 seconds. The client also routes most
production searches to per-event addresses chosen after workflow start, so the
shared browser's startup state is not a valid construction invariant.

## Decision

`twitter.NewClient` validates static configuration and creates the HTTP
transport without network I/O. Invalid static configuration remains a fatal
worker-startup error. Remote readiness is evaluated by each `Search` call.

The worker always injects the constructed client into discovery. A starting,
failed, or unreachable browser makes the current activity attempt fail;
Temporal retry and later discovery attempts use the same client and observe
current service state without a worker restart.

## Consequences

- Worker and Twitter container startup order is irrelevant.
- A transient startup outage cannot permanently unwire one worker replica.
- Constructor success means configuration is usable, not that any browser is
  currently authenticated.
- Operational readiness continues to come from the Twitter service's
  `/health` and `/status` endpoints rather than an adapter snapshot.
