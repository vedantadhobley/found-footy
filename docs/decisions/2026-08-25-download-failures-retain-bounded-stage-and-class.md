# Download failures retain bounded stage and class

## Context

`DownloadAndStage` is one retry unit containing media resolution, scratch-file
creation, CDN byte download, ffprobe, and Garage staging. FF-002 correctly
turned an exhausted activity into a terminal candidate result, but persisted
only `outcome_class=failed` and `reject_reason=download_error`. Raw Temporal and
Loki errors could identify the cause while retained; Postgres could not.

The 2026-08-22 through 2026-08-25 production sample made the gap material.
All 1,624 `download_error` candidates were variant-fetch HTTP 403 responses
from `video.twimg.com` after four attempts. The sample contained no exhausted
resolve, timeout, probe, scratch, or staging failures. A durable taxonomy is
required before changing download or retry policy.

## Decision

Retryable `DownloadAndStage` errors cross Temporal as application-error type
`video_download_failure`. Its bounded detail contains only:

- stage: `resolve`, `scratch`, `cdn_download`, `probe`, `staging_upload`, or
  the workflow fallback `activity`;
- class: one registered value such as `forbidden`, `rate_limited`, `timeout`,
  `transport`, `invalid_response`, `stream`, `filesystem`, `probe_failed`,
  `storage`, or `unknown`.

The error remains retryable. After the existing four attempts exhaust, new
EventWorkflow histories keep `outcome_class=failed` and
`reject_reason=download_error`, then persist the detail under
`outcome_detail.failure`. Raw error text and signed media URLs remain only in
Temporal history and structured logs.

The `ff-060-download-failure-detail` version marker leaves histories that
started before this change on their original terminal payload. Temporal-owned
timeouts and errors without valid typed detail persist as bounded
`activity/timeout` or `activity/unknown`. No schema migration is required.

## Consequences

- Operators can group future terminal failures from Postgres without Loki
  archaeology or message parsing.
- Existing retry counts, terminal rejection rules, candidate isolation, and
  accepted-media behavior do not change.
- The historical 1,624 rows are not rewritten. Their retained logs establish
  the initial all-`cdn_download/forbidden` distribution.
- A cookie-authenticated, alternate-variant, or other CDN-denial recovery path
  remains separate work based on measured post-rollout data.

This refines [FF-002's terminal result](./2026-08-16-video-failures-are-terminal-results.md)
and [FF-029's retryable CDN denial](./2026-08-17-cdn-download-denial-is-transient.md)
without changing either decision's retry or lifecycle invariants.
