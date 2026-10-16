# Explicit CDN geographic denials are terminal candidate outcomes

**Status:** deployed October 16 as `6ffee6b`; natural-denial validation pending.
See the [rollout record](../history/cdn-denial-rollout-2026-10-16.md).

## Context

[FF-029](./2026-08-17-cdn-download-denial-is-transient.md) made video-CDN
HTTP 403 retryable because the status alone cannot establish geographic
restriction. Retrying resolves the tweet again and can refresh a variant URL.
That rule still applies to unexplained denials.

The [October 15 investigation](../design/audits/cdn-and-discovery-gaps-2026-10-15.md)
found two Brunner source objects that explicitly returned plain-text
`Geo Forbidden`. High/low MP4 and HLS were denied from the application's egress,
while a same-path-family control succeeded. Six posts referenced one of those
objects; seven failed candidates did not mean seven independent videos.

The adapter discarded all denial bodies, so durable outcomes could not
distinguish this evidence from generic forbidden responses.

## Decision

For CDN HTTP 403 only, recognize a complete `text/plain` body whose trimmed
value is exactly `Geo Forbidden`. Permit ordinary media-type parameters.
Inspect at most 256 bytes plus one overflow byte, with a two-second read bound
that cancels the request. Parent cancellation remains effective. Empty,
oversized, truncated, stalled, non-text, and unknown bodies retain the existing
retryable `ErrCDNForbidden` classification. Do not guess from uploader, URL
family, status alone, or a substring.

An explicit match returns typed `ErrCDNGeoRestricted`. `DownloadAndStage`
maps it to the existing nil-error rejected outcome with stable reason
`cdn_geo_restricted`. The current EventWorkflow candidate writer persists that
reason; hashing, inference, staging and further automatic download attempts do
not run for that candidate. Scratch cleanup still runs.

This is a denial from the current egress, not a statement that the clip is
unavailable everywhere or forever. It does not establish the country policy or
whether an authenticated browser elsewhere could play it.

Denial errors retain only their typed class and variant host. Raw bodies and
signed URLs are not added to logs, Temporal payloads, or PostgreSQL. The stable
reason is the diagnostic evidence; no response-body archive is required.

## Consequences

- Unexplained CDN 403 still gets four resolve-and-download attempts, with
  bounded `cdn_download/forbidden` failure detail after exhaustion.
- Metadata HTTP 403 retains its existing `geo_restricted` behavior, separate
  from the new explicit CDN response. This does not add metadata evidence.
- Existing rejected-outcome handling works in both direct EventWorkflow
  processing and the retained VideoWorkflow compatibility path. No workflow
  command, version marker, schema, configuration, or public API changes.
- Existing candidate rows are not reclassified. Old failure bodies were not
  retained, so their cause cannot be inferred from a generic 403.
- No cookie fallback, alternate variant, HLS downloader, geographic routing,
  provider-object cache, or shared X quota controller is introduced. Those
  remain separate evidence-led work under FF-089 and FF-038.

Adapter tests cover exact versus ambiguous bodies, byte/time bounds, unchanged
non-403 statuses, and denial-data exclusion. Activity and workflow regressions
cover terminal persistence and skipped downstream work; existing generic-403
tests still require the full retry budget. Historical recovery remains separate.
