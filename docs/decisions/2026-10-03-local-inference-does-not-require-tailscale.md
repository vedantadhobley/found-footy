# Local inference does not require Tailscale

## Context

After the October 3 host restart, both workers repeatedly exited because
`control-joi.luv` could not resolve. Tailscale's startup arguments omitted its
saved compute-subnet advertisement. The gateway and all five Joi models were
ready, but the absent split-DNS route blocked the mandatory model probe and
therefore every worker responsibility, including fixture polling.

## Decision

Dev and prod worker Compose services map `control-joi.luv` to Docker's
`host-gateway` with `extra_hosts`. Requests retain their hostname and reach
host-published Caddy, which forwards them to Control's managed gateway.
The endpoint, model ID, admission limits, and application binaries retain
their existing contracts. No private model backend is exposed to Found Footy.

## Consequences

Local inference no longer requires Tailscale or its DNS configuration.
This deployment mapping assumes Caddy and the workers share a Docker host.
Moving either service requires reviewing the route. Other projects and remote
clients still depend on their own DNS and networking contracts; this record
does not establish a workspace-wide DNS policy.

A configuration-only recovery recreates the workers with the exact deployed
image and fleet-image tag. Verify a disposable container can reach `/ready`
and `/v1/models` through this mapping first, then verify worker startup and
fresh scheduled polling. A later full application release retains the mapping
from Compose. See the [deployment ledger](../deployment.md).

## Acceptance

On October 3 at 18:04:39 UTC, the approved worker-only recreation applied the
mapping while retaining application image
`626469349dcddf1d1441df1f7096a219b3f554a9`. Both workers resolved the gateway to
the local Docker host, connected to Gemma 12B and Temporal, and showed zero
restarts. Fresh fixture polls, rendered event searches, and a natural promoted
candidate confirmed processing. Other Found Footy services and Control kept
their existing containers. Tailscale recovery occurred independently.
