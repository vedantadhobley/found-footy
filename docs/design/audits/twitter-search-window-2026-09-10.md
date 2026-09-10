# Twitter search-window experiment — 2026-09-10 UTC

## Scope and disposition

Offline experiment for [FF-091](../../todo.md#ff-091--moving-search-age-window-can-skip-outage-period-posts).
Production still uses a moving three-minute cutoff. The initial offline
experiment changed no runtime behavior and made no live X search or extra
download/vision call. The subsequent local implementation is recorded in the
[FF-091 decision](../../decisions/2026-09-10-search-window-follows-first-observation.md);
it is not deployed. The baseline and synthetic observations below remain the
experiment's evidence, not a replacement for that as-built contract.

The tests support replacing the moving cutoff with an immutable event-relative
boundary. They do **not** establish the best pre-observation buffer, prove that
three known tweets imply complete coverage, or measure live recall/network cost.

## Current behavior and observed motivation

[EventWorkflow](../../../internal/workflow/event.go) passes `MaxAgeMinutes`
on every search. The [decoder](../../../internal/twitter/search_extract.go)
calculates age at extraction time, so the effective lower bound advances even
through an outage. FF-061 preserves the usable-attempt budget, not time coverage.

The September 9 Eastern / September 10 UTC read-only match-day inspection found
20 HTTP 429 probes across five events from 00:26:40–00:31:00 UTC. Each affected
event subsequently completed its 15 usable observations. This demonstrates
recovery, not retrieval of every post published during that interval.

The earlier worker-log sample contained 281 measured probes. First logical
attempts accounted for 20 `age` stops. After attempt one:

| Result | Probes |
|---|---:|
| Usable, stopped on age | 209 |
| Usable, stopped on consecutive seen | 26 |
| Usable, stopped on scroll cap | 4 |
| Unavailable | 22 |

Thus age ended 209/239 successful later searches (87.4%), not just first
searches. These are point-in-time log aggregates, not a saved ordered feed.
No particular missing unique goal clip was established by those summaries.

The [real scroll loop](../../../internal/twitter/search_scroll.go) processes
each tweet ID once per scan. It skips promoted entries, checks age, then filters
non-video/truncated-ID entries, then checks known IDs. The default early stop
is already three distinct known video tweets, with a reset on a new eligible
video. Ignored text posts and repeated DOM entries do not reset the counter.
An old organic text post can trigger the age stop before the video filter.

## Experiment

The test-only [model](../../../internal/twitter/search_window_model_test.go)
compares four policies on the same synthetic timestamped DOM batches. Each has
its own candidate history; one policy's discoveries cannot seed another's
already-seen set. To isolate the time policy, all variants retain the current
filter ordering, including the timestamp stop on organic non-video posts.

| Policy | Earliest timestamp | Known-video stop |
|---|---|---|
| Rolling baseline | Extraction time minus 3 minutes | Three |
| Fixed buffered | Event `first_seen_at` minus 3 minutes | Three |
| Fixed exact | Event `first_seen_at` | Three |
| Bounded reference | Event `first_seen_at` minus 3 minutes | Disabled |

The reference still has the scroll cap and all other filters. It is **not** a
ground-truth feed or a production recommendation. Reaching the end of a captured
input is marked `capture_end`, not falsely reported as exhausted X results.

The [policy cases](../../../internal/twitter/search_window_test.go) produce
these distinct synthetic candidates over each entire scenario:

| Scenario | Rolling | Fixed buffered | Fixed exact | Reference |
|---|---:|---:|---:|---:|
| Normal cadence; one post 10 seconds before observation | 4 | 4 | 3 | 4 |
| Five-minute outage | 2 | 4 | 4 | 4 |
| First probe unavailable; eventual first usable search | 1 | 2 | 2 | 2 |
| Delayed startup; one post 30 seconds before observation | 1 | 2 | 1 | 2 |
| Three known videos after an earlier partial scan | 3 | 3 | 3 | 4 |
| Known IDs absent; subsequent scan reaches its cap | 3 | 3 | 3 | 3 |

These are constructed counterexamples, not percentages of real missed videos.
The outage case puts two unseen posts below the moving boundary when recovery
succeeds. Fixed variants retain them. The partial-scan case previously saw only
the first three posts; every seen-three variant stops before an older unseen
fourth post that is still inside its time window.

The [conformance tests](../../../internal/twitter/search_window_conformance_test.go)
run the actual `Service.scrollAndExtract` on fake `Page.Evaluate` batches and
compare URLs, stop reasons, extraction/scroll counts, and parsed/video counters
against the rolling model. They also pin promoted handling, missing timestamps,
organic text age stops, ignored text/repeated DOM entries, and empty-after-scroll.
No browser starts; JavaScript selector accuracy is outside this test seam.

Deterministic boundary tests include equality, one nanosecond before/after,
future/malformed/missing timestamps, and missing first-seen rejection. A JSON
checkpoint round trip between probes preserves the cutoff and per-policy seen
history. That is experiment-state coverage, **not** a new SQL or Temporal replay
test. Existing workflow tests separately cover current outage budgets/recovery.

Extraction and scroll counts describe simulated work only. A scroll is not an
X HTTP request: hydration, prefetch, caching, and pagination are not modeled.

## Design implications

1. Drop the **moving** three-minute limit. Set a stable earliest timestamp once
   for an event's discovery and reuse it across searches and recovery.
2. Keep a pre-observation allowance unless consciously accepting that loss.
   `first_seen_at` is when our poll first records the event, not the instant the
   goal happened. Three minutes is a comparison value, not an optimized buffer;
   even it cannot cover arbitrarily late vendor reporting.
3. Treat three-seen as a cost-saving heuristic, not a coverage guarantee. The
   current rule can hide an older unseen post after partial earlier results.
   Before promising recovery coverage, define when prior scan evidence makes
   early stopping acceptable. Merely changing the count cannot prove completeness.
4. Preserve request deadlines and the hard scroll cap. A fixed floor becomes
   farther away over time; missing known IDs can require more pagination. An old
   organic entry above newer results would also invalidate any first-old stop.
5. Keep local timestamp checks. Do not add `since:`/`until:` or change player/team
   query construction. Shared account admission remains separate FF-038 work.

## Implementation surface identified by the experiment

This section preserves the pre-implementation assessment. The decision linked
above now defines the local implementation: durable event-row timestamps,
versioned fixed-window requests, and conservative failed-run recovery.

The [monitor input](../../../internal/activity/monitor/emission.go) already
passes the stored `FirstSeenAt` into EventWorkflow, including recovery spawns.
Use that immutable observation, not workflow startup time, match minute, or the
first successful search. Old histories can have zero `FirstSeenAt`; define an
explicit compatibility path instead of silently replacing it with `now`.

Future runtime work needs a typed absolute boundary through the workflow,
SearchTweets activity, shared HTTP contract, client, and scroll loop. The
existing [recovery metadata](../../../internal/activity/discovery/candidates.go)
can retain the selected boundary without a new table, but that persistence and
Temporal versioning still need implementation/testing. Preserve maintenance's
independent 24-hour canary window. Do not combine relative and absolute limits
silently or recompute an old event's chosen buffer after configuration changes.

Current durable candidates contain returned URLs and age-at-discovery, not the
ordered feed below an early stop. Historical SQL cannot reconstruct those
unobserved posts. Any later live experiment needs separately approved, bounded
captures beyond the normal stop, with both policies replayed over **one** feed
and actual SearchTimeline requests counted. Dev and prod share account quota;
two competing live searches would both spend quota and confound the comparison.

Validation command inside the pinned Go tooling container:

```sh
go test -buildvcs=false -short -v -run TestSearchWindow ./internal/twitter
```

This command, `make check-short`, and the race-enabled window/current outage-
recovery tests passed in memory-capped, network-disabled tooling containers.
Changed-document local link targets and `git diff --check` also passed.

## Subsequent implementation verification

After the fixed buffered cutoff and guarded shortcut were approved, the runtime
tests reproduced every synthetic scenario using the real scroll loop. The
guarded implementation matches the bounded reference's candidate sets in this
corpus, including the earlier partial-scan counterexample. This is still not a
live recall benchmark. HTTP tests cover both handler acknowledgements and client
rejection of ignored/mismatched windows; workflow and real-Postgres tests cover
durable initialization, stale writes, old histories, and recovery boundaries.

Full `make check` and targeted race checks passed. Production was not changed,
and no live search or historical replay was started. The
[issue register](../../todo.md#ff-091--moving-search-age-window-can-skip-outage-period-posts)
owns commit/rollout status and natural validation.
