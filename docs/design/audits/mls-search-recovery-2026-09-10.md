# MLS search and recovery audit — 2026-09-10

## Scope

Read-only audit of the 14 retained MLS fixtures with September 9 Eastern
kickoffs: `1490451`–`1490462`, `1490464`, and `1490465`. SQL used league 253
and kickoff bounds `[2026-09-09T04:00Z, 2026-09-10T04:00Z)`. Retained worker
logs cover `[2026-09-09T23:00Z, 2026-09-10T05:10Z)`; targeted REST reads
followed the [05:11 UTC rollout](../../history/search-window-and-cleanup-rollout-2026-09-10.md).

The searches ran on `a7c9f53`, before FF-091/FF-073 deployed. This is baseline
and recovery evidence, not natural validation of the newly deployed changes.
No extra vendor/X query, download, vision request, replay, or data repair ran.

## Completion and public output

- All 14 fixtures reached completed state. Every fixture's surviving goal count
  matched its score; this does not prove that every scorer attribution is right.
- There are 50 surviving events: 46 goals, three red cards, one missed penalty.
  All triggered discovery and reached the public `complete` phase.
- Every surviving event completed 15 usable searches. None exhausted its
  unavailable-search allowance or remained in flight.
- Eight historical events are removed. Five started discovery before removal;
  their workflows ended as `event_removed` after 17 usable probes in total.
- Of the 50 surviving discoveries, 33 surfaced assets, nine found candidates
  but accepted none, and eight found no candidates.
- The API exposes 38 videos across 33 events. SQL retains 54 active shares;
  public singleton-pruning explains why active storage and returned video counts
  are different measures. No video semantic-quality review was performed here.

Surviving workflow durations ranged from 14m28s to 20m30s. That is the whole
discovery window, not time until the first video. Final-search completion is not
proof of complete X coverage or of a correct video for every event.

## Search failures and shared rate evidence

SQL progress and retained workflow measurements reconcile: **821 probes**,
comprising 767 usable observations and 54 unavailable observations (6.6%).
The usable count includes the 17 probes from subsequently removed events;
the remaining 750 are the 50 surviving events' full budgets.

Thirty events encountered at least one unavailable probe. The matching
HTTP-client failure logs classify those 54 observations as:

| Evidence | Probes |
|---|---:|
| SearchTimeline HTTP 429 | 35 |
| `NS_BINDING_ABORTED` | 10 |
| `NS_ERROR_ABORT` | 8 |
| Unknown feed timeout | 1 |

The browser abort labels alone do not identify a common cause. Do not call all
54 observations Twitter rate limits or independent lost videos.

Every recorded 429 reported limit `50`, remaining `0`. Six distinct reset
values occur:

| Reported reset UTC | First–last observed 429 UTC | Probes |
|---|---|---:|
| 00:31:01 | 00:26:40–00:31:00 | 20 |
| 01:32:19 | 01:32:20 | 1 |
| 02:02:43 | 02:01:59–02:02:51 | 3 |
| 02:17:43 | 02:17:41 | 1 |
| 02:32:45 | 02:32:33–02:32:50 | 3 |
| 03:03:10 | 03:01:28–03:03:18 | 7 |

Dates in this table are September 10 UTC. Some failure observations arrive
after their reported reset. Reset headers therefore cannot be used as an
unconditional immediate-retry signal; request timing, propagation, and clock
differences remain possible explanations.

After logical attempt one, 712 usable probes stopped as follows: age 636,
consecutive-seen 71, scroll cap five. Age accounts for 89.3% of those later
probes. This extends the smaller pre-implementation sample that motivated
FF-091. A moving age bound was not limited to the first scan. No retained
ordered timeline proves which unseen posts lay below those stops.

## Candidate outcomes

The batch retained 1,002 candidates: 794 rejected, 121 duplicate, 56 promoted,
25 superseded, and six failed. No candidate remains pending. These are durable
candidate outcomes, not independent media variants or public clip counts.

The largest rejection groups are narrow aspect ratio (438), not-soccer vision
votes (125), and excessive duration (118). Without reviewing the media, this
does not justify changing acceptance gates or treating every rejection as lost
goal footage.

Five terminal failures are the known FF-089 `cdn_download/forbidden` class.
They affect Santiago Rodriguez 49′, Bruno Damiani 61′, Luighi 71′,
Max Floriani 90+2′, and Cédric Bakambu 90+4′. These are separate from search 429s.

One Bruno Damiani 49′ candidate ended with `vision_error` at 01:26:52 UTC.
Its event is `ea7f8ccf-6267-4bf2-b072-180852ebdedb`; durable detail contains
`failure.stage=model_request` and `failure.class=timeout`. This is natural
evidence that FF-087 now preserves an actionable terminal cause. It does not
identify how much time was gateway admission versus inference, or validate
every failure subtype. The event still surfaced other assets.

## Next design boundary

FF-038 remains the shared-account admission issue. This evidence supports
designing coordinated backoff, not adding an independent sleep to each browser.
The existing backlog assigns ownership to the fleet-controller boundary;
settle the smallest implementation there before expanding infrastructure.

An offline controller model can cover concurrent 429s, future/missing/expired
reset headers, one recovery probe instead of synchronized retries, cancellation,
and worker restarts. Deliberate waiting must not consume a usable observation
or move the FF-091 timestamp. Actual timeline requests still need measurement:
one search/scroll is not one request, and `50` is not a universal budget for
application-level searches or every X endpoint.

Keep CDN recovery, vision admission, query semantics, and keeper quality as
separate issues. No production policy changed during this audit.

## Reproduction sources

- SQL: `fixtures`, `events`, `event_downstream_workflows`,
  `event_search_candidates`, and active `video_shares`, joined through fixture
  IDs under a read-only transaction. Read progress from downstream metadata.
- Loki selector: `{compose_project="found-footy-prod",compose_service="worker"}`.
  HTTP records use `| json | action="twitter_search_failed"`; SDK timing
  records use `|= "event_search_measured"`. The 1,000/2,000 query limits exceeded
  their 54/821 returned records; the event/fixture set matched the SQL scope.
- REST: `/api/v1/fixtures?ids=...` for the 14 IDs above, counting exposed videos
  separately from SQL shares. Do not follow media redirects for this audit.

Current work ownership remains in the [issue register](../../todo.md).
