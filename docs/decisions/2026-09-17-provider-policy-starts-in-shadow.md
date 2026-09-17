# Provider policy starts in shadow without disabling atomic writes

**Status:** Implemented locally; application rollout pending.

## Context

The durable health collector and atomic write boundary have regression coverage,
but the new freshness, correlation, and cause-specific recovery rules have not
yet run through a production match window. Enforcing a false restriction could
delay a legitimate correction or fixture completion. The older stateless shadow
audit is not validation of this newer policy.

## Decision

`PROVIDER_INTEGRITY_MODE` is a worker-only setting shared by active polling,
staging, and ingestion. It defaults to `shadow`; `enforce` requires an explicit
configuration change. Invalid values fail worker startup.

Both modes collect the same durable health recommendations. Under the existing
transaction locks, writers compose provider, fixture, and local policy:

- `shadow` records the recommendation but applies ordinary `trusted` semantic
  permissions. Existing wire validation, score-backed absence protection,
  ownership checks, stale-plan rejection, atomicity, and retry receipts remain.
- `enforce` applies the recommended permissions. Its semantics are unchanged
  from the [atomic boundary decision](./2026-09-16-provider-policy-and-mutations-share-one-transaction.md).

The existing mutation receipt records writer, mode, each policy input, final
recommendation, and the plan operations the recommendation limits. Its original
effect fields record actual changes. Limited fixture refreshes/corrections are
not claims that every supplied field would change or be blocked; positive-only
merges can still apply safe fields. Absence votes are not removal counts, and a
held completion check is not proof that completion was otherwise ready.

Retries return the original receipt, including its original mode and effects,
even across a mode change. Old receipts and stale plans have no decision rather
than a fabricated historical mode. Health cause/recovery transitions stay in
health poll receipts; changing mode does not reset incident evidence.

## Consequences and rollout gate

No new migration, service, schedule, provider request, or public message is
needed. Shadow does not protect production from semantic feed regressions; it
measures the proposed protection. Collection failures still stop that active
poll because the transaction requires a coherent health receipt.

Run at least one complete match window in shadow. Review fixture versus provider
scope, each restricted plan, recovery delays, and false restrictions around
period changes, cancellations, scorer corrections, and completion. If no useful
anomaly/recovery occurs, a quiet window alone does not validate those paths.
Enable enforcement only after that review and separate production approval.

Shadow changes canonical data differently from enforcement. Its decisions are
per-observation evidence, not an exact simulation of the fixture/event history
that enforcement would have produced. Durable incident baselines nevertheless
survive canonical shadow writes and prevent a repeated regression from erasing
its own warning.
