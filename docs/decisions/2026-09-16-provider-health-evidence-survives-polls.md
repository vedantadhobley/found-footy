# Provider health evidence survives polls and retries

**Status:** Production schema prepared September 17 UTC; collector deployed in
shadow as `abfea7a` at 02:35 UTC. Natural match-window validation remains open.

## Context

The [September 16 stalled-feed incident](../incidents/2026-09-16-api-football-stalled-fixture.md)
exposed a gap the stateless semantic evaluator cannot detect: a successful,
unchanged running-clock response can remain stale for many polling cycles.
Temporal durability does not supply the cross-execution observation history
needed to identify that condition or remember independent recovery causes.

## Decision

Implement the [provider-health evidence contract](../provider-health.md) in the
existing active poll. This decision established the evidence layer first; the
later [atomic mutation-boundary decision](./2026-09-16-provider-policy-and-mutations-share-one-transaction.md)
makes its policy enforceable across all provider writers.
The [rollout-mode decision](./2026-09-17-provider-policy-starts-in-shadow.md)
keeps that policy advisory by default while this durable evidence is validated.

- Use a logical workflow-run receipt, immutable observation time, and typed
  progress cursors. Retries and delayed responses cannot advance recovery.
- Retain failed fetch attempts before a successful retry can hide them.
- Preserve separate availability, freshness, and reason-specific consistency
  causes. Keep causal facts until each cause independently resolves.
- Use the agreed three-minute interior and ten-minute boundary hypotheses,
  ninety-second continuity bound, two-fixture/two-minute scope correlation,
  and independent freshness/provider recovery rules.
- Store current evidence in provider/fixture health tables. Keep compact poll
  receipts and transition history for fourteen elapsed days. Do not store a
  full raw snapshot every thirty seconds or reuse the public event log.
- Keep current polling, retries, canonical reconciliation, completion, search,
  and frontend contracts unchanged. The new workflow activity is versioned.

## Consequences

This adds three internal tables and one pre-reconciliation activity, not a
service or schedule. Evidence transactions serialize on a provider row; network
work and canonical fixture reconciliation remain outside that lock.

The recorder survives worker restarts and retains the original contradiction.
With enforcement enabled, the subsequent guarded mutation boundary prevents a
conflicting observation from overwriting the canonical value. Local collection failure closes the
active write gate for that poll; a loss before persistence remains an
observation gap.

Operator resolution, semantic recovery across already-removed event generations,
dedicated health metrics, and delayed-goal search windows remain separate
explicit work. The migrations rewrite no existing fixture, event, or media
records.
