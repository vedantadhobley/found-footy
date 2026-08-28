# Architectural decisions

This directory is the current decision-log entry point. Code and focused
as-built ledgers define what exists; decisions explain why a material contract
changed. Active bugs and deferred work belong in [`todo.md`](../todo.md), not
here.

## Current state

All decisions through 2026-08-16 are preserved, newest first, in the
[frozen archive](./archive-through-2026-08-16.md). The old
[`docs/decisions.md`](../decisions.md) path is a compatibility index that keeps
its historical heading anchors valid.

New decisions after the frozen archive:

- [Accepted candidates commit as one placement](./2026-08-28-accepted-candidates-commit-as-one-placement.md) — FF-011/FF-048/FF-066 make attribution, popularity, asset/share mutation, supersession, derived rank, and invalidation one retry-safe contract.
- [Tiered perceptual dedup requires local or sustained evidence](./2026-08-28-tiered-perceptual-dedup-requires-local-or-sustained-evidence.md) — production-v2 boundaries select replay-safe 12/30/3 or 16/50/5 matching while retaining the three-second hash-admission floor.
- [Exact-byte followers inherit the representative terminal result](./2026-08-26-exact-followers-inherit-representative-outcome.md) — FF-065 preserves one validation path per MD5 but records followers as duplicates only after an asset wins.
- [Download failures retain bounded stage and class](./2026-08-25-download-failures-retain-bounded-stage-and-class.md) — FF-060 carries retryable download failure evidence through Temporal and persists it without raw errors or a schema migration.
- [Found Footy uses Control's model request contract](./2026-08-25-control-model-request-contract.md) — vision sends public `reasoning_effort: none`, removes backend-private template controls, and inherits model-owned sampling defaults.
- [Terminal observation grace bounds fixture completion](./2026-08-25-terminal-observation-grace-bounds-completion.md) — FF-063 replaces permanent score-parity completion gating with one hour of terminal observation plus settled event/downstream gates.
- [Found Footy uses Control's canonical Joi gateway](./2026-08-25-control-joi-is-production-inference-route.md) — production moves from the legacy `joi.luv` identity to `control-joi.luv` without changing its pinned model, concurrency, or application release.
- [Removed event reappearance starts a new generation](./2026-08-24-removed-event-reappearance-starts-new-generation.md) — FF-062 keeps VAR tombstones immutable while allowing returned provider evidence through a fresh debounce and downstream lifecycle.
- [Twitter search attempts require usable observations](./2026-08-20-twitter-search-attempts-require-usable-observations.md) — FF-061 classifies browser results, separates usable searches from bounded outage probes, and persists secret-free response evidence.
- [Raw Firefox owns operator login](./2026-08-19-raw-firefox-owns-operator-login.md) — FF-059 separates raw-Firefox credential minting and read-only profile capture from Playwright search.
- [Twitter maintenance uses the static fallback](./2026-08-19-twitter-maintenance-uses-static-fallback.md) — FF-058 adds a fixture-independent forced-auth, cookie-sync, and live-search DOM canary without warming the dynamic event fleet.
- [Historical candidate repair reuses EventWorkflow](./2026-08-19-historical-candidate-repair-reuses-event-workflow.md) — exact terminal selectors become auditable pending work under a new deterministic checklist; the normal pipeline reprocesses them without a fresh search.
- [Winner state is derived from canonical scores](./2026-08-19-winner-state-is-derived-from-canonical-scores.md) — FF-055 derives played results from match or shootout scores and clears tied leaders; FF-063 later moved `PEN` decision state from a completion gate to audit evidence.
- [Thin entry points and in-package ownership splits](./2026-08-18-thin-entrypoints-and-in-package-ownership-splits.md) — FF-045 moves worker composition out of `cmd`, splits large files without changing package or Temporal identity, and deletes caller-proven residue.
- [Dense frame hashing uses a versioned bounded working image](./2026-08-17-dense-hashing-uses-versioned-bounded-working-image.md) — FF-041/FF-005 preserve dHash while bounding 4K work, rejecting structurally short sequences, and preventing cross-version comparisons.
- [Live evidence sets landscape aspect admission to 1.73–1.82](./2026-08-17-live-evidence-sets-landscape-aspect-admission.md) — FF-053 admits legitimate 1.739 Elche clips without widening into the known 1.60–1.72 letterbox band.
- [Candidate terminal state is a workflow invariant](./2026-08-17-candidate-terminal-state-is-a-workflow-invariant.md) — FF-034 couples complete evidence to an idempotent terminal UPSERT and blocks parent success until it lands.
- [The read API does not connect to NATS](./2026-08-17-read-api-does-not-connect-to-nats.md) — FF-043 keeps event publication in workers and direct subscription in the BFF without coupling REST startup to the broker.
- [Configuration is binary-owned and fails before external work](./2026-08-17-configuration-is-binary-owned-and-fail-fast.md) — FF-035 typed profiles, semantic validation, and derived env/Compose contract.
- [Engineering gates use pinned tool versions](./2026-08-17-engineering-gates-use-pinned-tools.md) — FF-042 exact Go, golangci-lint, and Air versions plus commit/push check contracts.
- [Compose partitions the fixed ffmpeg host budget](./2026-08-17-compose-partitions-ffmpeg-host-budget.md) — FF-021 stack-wide CPU arithmetic and fixed production replica contract.
- [Stale EventWorkflow recovery requires Temporal progress proof](./2026-08-17-stale-event-recovery-requires-progress-proof.md) — FF-025 exact-run, two-snapshot termination and FF-007 re-drive contract.
- [Exact-byte ownership precedes dense video hashing](./2026-08-17-exact-md5-ownership-precedes-dense-hashing.md) — FF-022 single-claim hashing, claimant failover, and replay-compatible child retirement.
- [CDN download denial is transient](./2026-08-17-cdn-download-denial-is-transient.md) — FF-029 separates retryable variant-fetch 403 from terminal metadata 403.
- [Video redirect cache stays inside the presigned URL lifetime](./2026-08-17-video-redirect-cache-stays-inside-presign.md) — FF-028 derives safe redirect caching from the live S3 TTL.
- [Event sequences match stored identity instead of provider array position](./2026-08-17-event-sequences-match-stored-identity.md) — FF-027 brace, reorder, and removed-tombstone identity contract.
- [Browser death restarts the complete container unit](./2026-08-17-browser-death-restarts-container-unit.md) — FF-017 critical-child health, process exit, and Docker restart ownership.
- [Failed EventWorkflow executions resume durable progress](./2026-08-17-failed-event-workflows-resume-durable-progress.md) — FF-007 Workflow ID reuse, checkpoint, and recovery boundary.
- [Promotion retries complete ranking and staging cleanup](./2026-08-16-promotion-retries-complete-durable-tail.md) — FF-006/FF-023 durable-tail and dirty-signal contract.
- [Twitter client construction is independent of browser readiness](./2026-08-16-twitter-client-construction-is-readiness-independent.md) — FF-016 recovery from worker/Twitter startup races.
- [Event-browser names follow workspace order while labels authorize lifecycle](./2026-08-16-event-browser-names-follow-workspace-order.md) — FF-020 naming and release-selection correction to FF-001.
- [Exhausted video activities return terminal candidate results](./2026-08-16-video-failures-are-terminal-results.md) — FF-002 typed failure, cleanup, and Temporal replay contract.
- [Production releases use one immutable identity from a clean checkout](./2026-08-16-immutable-production-release-identity.md) — FF-019 release provenance and verification contract.
- [Score evidence gates goal removal and played-fixture completion](./2026-08-16-score-backed-goal-removal.md) — FF-014's goal-removal guard remains; FF-063 supersedes its permanent completion-parity boundary.

## New-decision format

Create one file per decision: `YYYY-MM-DD-short-slug.md`. Use one H1 and these
sections when they add value: context, decision, consequences, and superseded
contract. Link the new file at the top of this README. If it changes an old
decision, link both directions; never edit the old rationale into saying
something it did not say.

A decision records a material, landed architectural or behavioral choice. An
idea stays in a proposal. An unresolved defect stays in `todo.md`. Update the
relevant as-built ledger in the same change.
