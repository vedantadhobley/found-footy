# Issue validation — 2026-09-08

## FF-060 — download failures retain their actionable cause

**Status: closed for the diagnostic implementation.** CDN-denial recovery is
not solved; it is tracked separately as FF-089 in the
[active register](../todo.md#ff-089--video-cdn-denials-still-exhaust-download-retries).

The original August 22–25 sample contained 1,624 download failures among
11,018 candidates (14.74%) across 115 events, all video-CDN HTTP 403 after
four attempts. Four events lost their complete one- or two-candidate sets;
the same window had no exhausted resolve, timeout, scratch, probe, or Garage
staging failures. Release `e4ae2d7`
deployed on August 25 at 14:06 UTC. It preserves a bounded stage/class through
Temporal retry exhaustion and candidate persistence, without changing retries
or acceptance policy. See the
[failure-detail decision](../decisions/2026-08-25-download-failures-retain-bounded-stage-and-class.md).

The [September 8 audit](../design/audits/pre-rollout-evidence-2026-09-08.md)
verified all 1,149 `download_error` outcomes from September 1 through the
September 8 pre-rollout cutoff retained:

```json
{"failure":{"stage":"cdn_download","class":"forbidden"}}
```

This meets the natural-failure acceptance gate. Raw errors and signed media
URLs are not persisted in that bounded detail. Vision failures still discard
their cause at a different boundary; FF-087 owns that gap.
