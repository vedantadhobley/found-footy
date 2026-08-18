# Promotion retries complete ranking and staging cleanup

## Context

`PromoteAndPersist` copied a candidate from `staging/` to `assets/`, inserted
the deterministic asset and share rows, and rebalanced event ranks. It never
deleted the promoted staging object. Adding a delete only at the end would
create a retry trap: deletion can succeed while the activity completion
response is lost, leaving a retry unable to copy from its now-missing source.

The existing retry guard also treated a share inserted by a failed prior
attempt as full activity completion. If that prior attempt failed during rank
rebalance, the retry returned early and made the stale rank order permanent.

The target rebuild plan specifies one atomic mint-and-rank database
transaction. The shipped system deliberately retains separate asset insert,
share insert, and rank-rebalance operations inside the EventWorkflow's
serialized consumer. This change repairs retries within that as-built model;
it does not claim atomic parity with the target plan.

## Decision

`PromoteAndPersist` completes this ordered durable tail:

1. Derive the asset UUID and destination key from the event and MD5.
2. Query the asset before copying. A matching existing row proves that an
   earlier attempt completed its copy-before-insert sequence, so skip copy.
3. If no row exists, copy staging to the destination and insert the asset.
   Validate an insert conflict against the same immutable identity.
4. Ensure one share exists. Treat an existing share as progress, not an early
   return.
5. Always rebalance event ranks.
6. Delete staging last. The delete remains idempotent.

A deterministic asset whose event, fixture, bucket, key, or MD5 differs from
the expected immutable identity fails closed. A successful completion returns
`Minted=true` whenever the durable share exists, including a retry that found a
share inserted by its failed prior attempt. The workflow never observed the
failed attempt and therefore still owes its `event.video` dirty signal.

## Consequences

- Successful promotion reclaims its staging object without relying on a
  future sweep.
- A lost delete response cannot force a retry to recopy a missing source.
- A transient rebalance failure is retried and repaired without minting a
  second share.
- The EventWorkflow emits after the complete persistence tail. An external
  workflow re-drive can emit another dirty signal, but consumers refetch
  current state and do not treat the signal as the asset payload.
- Abnormal termination can still orphan staging objects outside this normal
  path. FF-024 tracks the separate bounded-sweep hardening.
- Existing production staging leaks are not repaired by deployment. Any
  inspection or deletion is a separate explicitly approved production action.

## Superseded contract

This refines the frozen
[`#164b persistence decision`](./archive-through-2026-08-16.md#2026-08-03--164b-consumer-queue-persist-activities--a-combine-deviation),
which described retry copy as a harmless overwrite and an existing share as a
terminal idempotent no-op. Current behavior is recorded in the
[`EventWorkflow` ledger](../orchestration/event.md#eventworkflow) and tracked as
[`FF-006`](../history/issue-register-2026-08-17.md#ff-006--promoted-clips-retain-staging-objects)
and
[`FF-023`](../history/issue-register-2026-08-17.md#ff-023--promotion-retry-can-skip-rank-rebalance).
