# Provider policy and canonical mutations share one transaction boundary

**Status:** Deployed as `abfea7a` on September 17 at 02:35 UTC; atomicity is active,
semantic policy remains in shadow pending match-window review.

The [subsequent rollout decision](./2026-09-17-provider-policy-starts-in-shadow.md)
defaults semantic policy to shadow. The restriction rules below describe opt-in
enforcement; transaction locks, stale checks, and retry receipts stay active in
both modes.

## Context

The durable health collector can identify stale or contradictory API-Football
observations, but a policy checked before a normal repository write is still a
check-then-act race. Active reconciliation also used separate transactions for
the fixture refresh, event votes, audits, downstream checklist rows, and
completion. Staging and daily ingest used independent provider writers.

A newer health generation or another provider observation could therefore land
between assessment and mutation. Activity response loss could also repeat only
part of a logical reconciliation operation.

## Decision

All provider-derived fixture writes use one `providerintegrity.MutationStore`.
The caller builds an immutable plan from a captured canonical baseline; Postgres
then derives the effective policy and commits every permitted mutation in one
short transaction.

- Lock the single provider policy row first. Health collection takes
  `FOR UPDATE`; fixture writers take `FOR SHARE`, which preserves concurrent
  writes to different fixtures while excluding a generation change.
- Take a fixture-scoped advisory lock and fixture row lock next. Reject a plan
  whose lifecycle or `last_polled_at` baseline changed.
- Active reconciliation also requires the exact finalized health poll ID,
  observation time, and provider generation. The collector verifies its own
  captured fixture baselines after taking the provider lock.
- Compose provider, fixture, and local observation policy under those locks.
  `rejected` permits no provider mutation. `positive_only` permits additions
  and credible forward progress but no absence vote, regression, terminal-
  observation timestamp change, or completion. `trusted` permits ordinary
  reconciliation.
- Commit fixture fields, event inserts/updates/votes, required audit rows,
  downstream checklist rows, and completion together.
- Store a bounded per-observation/per-fixture result receipt in the same
  transaction. A Temporal activity retry returns the original effect set,
  including the fixture status-versus-structural notification classification.
- Perform Temporal starts, NATS publication, cancellation, and object-store work
  only after commit. Existing recovery paths make those effects retryable.

Staging and ingest use the same boundary. They do not require an active health
receipt, but they read current provider/fixture policy under the same lock and
combine it with their local stored-versus-observed verdict. Old Temporal
histories retain their original commands through `provider-mutation-guard-v1`.

## Consequences

The mutation policy is enforced at the only boundary that can make the decision
race-free. The implementation adds one internal receipt table; it does not add
a service, schedule, provider call, public API field, or NATS subject.

Different fixtures remain concurrent. Health commits briefly wait for in-flight
fixture commits, and fixture commits briefly wait for health generation changes.
No lock crosses network or external-service I/O.

A collector failure or stale comparison now fails closed for that active poll.
The next scheduled poll rebuilds from current state. Staging and ingest can still
accept monotonic additions during a provider-wide restriction, but cannot
silently regress an existing fixture.

Raw correction evidence, audited operator decisions, incident-linked search
recovery, and dedicated provider-health metrics remain separate work. They do
not weaken this transaction invariant.
