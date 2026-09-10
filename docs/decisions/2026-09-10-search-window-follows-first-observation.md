# Search windows follow the first event observation

## Context

FF-061 protects usable-search counts during X outages, but each retry still
applied a moving three-minute age limit. A post could leave that window before
any successful search saw it. The
[FF-091 experiment](../design/audits/twitter-search-window-2026-09-10.md)
also reproduced three known video IDs hiding an older unseen candidate after
an earlier capped scan.

## Decision

New EventWorkflow histories use one shared `SearchWindow` containing an absolute
`earliest_tweet_at` and boolean `allow_seen_stop`. The recovery activity initializes
the timestamp from the stored event's `first_seen_at` minus the configured
lookback, default three minutes, before the first search. Existing downstream
checklist JSON metadata stores it; there is no table, column, migration, new
activity, or service.

`DISCOVERY_MAX_AGE_MINUTES` and the recorded config field retain their legacy
names for compatibility. New histories use them for the initial pre-observation
allowance. Initialization never overwrites an existing window. Config changes,
activity retries, and replacement executions cannot move that checklist's
boundary. Missing/malformed durable state fails explicitly. Missing old workflow
input `FirstSeenAt` is covered by the stored event timestamp, never current time.

The shortcut begins disabled. A rendered scan reaching the time boundary enables
it for the next probe. A permitted `consecutive_seen` stop preserves eligibility.
Every other stop, including cap, empty feed/page, unknown result, or unavailable
probe, disables it. Request deadlines and scroll caps remain. Exclusions always
skip already-owned candidates, even while the shortcut is disabled.

Progress stores next-probe eligibility alongside the monotonic usable/unavailable
counters. Older checkpoints cannot replace newer evidence; a write carrying a
different cutoff fails. A replacement execution restores the cutoff but starts
with the shortcut disabled: a failed run may have saved URLs from a later scan
without saving that scan's stop. Replay of the same run retains recorded activity
results and reconstructs eligibility from that run's history.

The POST request supplies either `window` or legacy `max_age_minutes`, never
both. The browser echoes the applied window on HTTP-200 results. For usable
results, the client requires the exact timestamp and permission to match. An
older browser that ignores new fields cannot silently grant false coverage.
Classified unavailable responses remain failure evidence even without an echo;
they cannot grant eligibility or contribute candidates.

The `ff-091-fixed-search-window` Temporal marker preserves old command inputs
for existing histories. Maintenance keeps its independent relative 24-hour
canary. The X query stays unchanged: time filtering remains local, with no
`since:`/`until:` operator. No frontend contract changes.

## Consequences and remaining limits

- Runtime, HTTP, workflow, and real-Postgres tests cover the demonstrated
  moving-window and capped-prefix counterexamples.
- Three minutes is an initial buffer, not an empirically optimal one. Very late
  provider reporting can still exclude earlier relevant posts.
- Early stopping still assumes chronological organic results. Delayed indexing,
  out-of-order entries, missing timestamps, and partial hydration prevent a claim
  of complete recall. Eligibility means an observed scan reached its boundary,
  not that X exposed every relevant post.
- Wider time coverage can require more pagination. Scrolls are not HTTP request
  counts; measure timeline traffic before claiming a rate/cost improvement.
- Shared account cooldown/admission remains separate FF-038 work.
- Roll out worker and browser images together in an approved quiet window.
  Existing event browsers retain their old image until removal; mismatched
  usable responses fail instead of silently reverting the new policy.

## Superseded contract

This replaces the moving bound in
[search-query D4](../design/proposals/twitter-search-query.md#d4--time-bounds)
for new event histories only. It extends
[FF-061's attempt accounting](./2026-08-20-twitter-search-attempts-require-usable-observations.md)
without changing its budgets.
