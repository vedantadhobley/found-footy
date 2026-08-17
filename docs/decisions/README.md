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

- [Event sequences match stored identity instead of provider array position](./2026-08-17-event-sequences-match-stored-identity.md) — FF-027 brace, reorder, and removed-tombstone identity contract.
- [Browser death restarts the complete container unit](./2026-08-17-browser-death-restarts-container-unit.md) — FF-017 critical-child health, process exit, and Docker restart ownership.
- [Failed EventWorkflow executions resume durable progress](./2026-08-17-failed-event-workflows-resume-durable-progress.md) — FF-007 Workflow ID reuse, checkpoint, and recovery boundary.
- [Promotion retries complete ranking and staging cleanup](./2026-08-16-promotion-retries-complete-durable-tail.md) — FF-006/FF-023 durable-tail and dirty-signal contract.
- [Twitter client construction is independent of browser readiness](./2026-08-16-twitter-client-construction-is-readiness-independent.md) — FF-016 recovery from worker/Twitter startup races.
- [Event-browser names follow workspace order while labels authorize lifecycle](./2026-08-16-event-browser-names-follow-workspace-order.md) — FF-020 naming and release-selection correction to FF-001.
- [Exhausted video activities return terminal candidate results](./2026-08-16-video-failures-are-terminal-results.md) — FF-002 typed failure, cleanup, and Temporal replay contract.
- [Production releases use one immutable identity from a clean checkout](./2026-08-16-immutable-production-release-identity.md) — FF-019 release provenance and verification contract.
- [Score evidence gates goal removal and played-fixture completion](./2026-08-16-score-backed-goal-removal.md) — FF-014 correctness guard and remaining terminal-reconciliation boundary.

## New-decision format

Create one file per decision: `YYYY-MM-DD-short-slug.md`. Use one H1 and these
sections when they add value: context, decision, consequences, and superseded
contract. Link the new file at the top of this README. If it changes an old
decision, link both directions; never edit the old rationale into saying
something it did not say.

A decision records a material, landed architectural or behavioral choice. An
idea stays in a proposal. An unresolved defect stays in `todo.md`. Update the
relevant as-built ledger in the same change.
