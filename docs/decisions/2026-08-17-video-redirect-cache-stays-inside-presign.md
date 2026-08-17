# Video redirect cache stays inside the presigned URL lifetime

## Context

The stable share endpoint returns a cacheable 302 to a short-lived Garage URL.
The play-latency decision fixed the redirect cache at five minutes, while the
S3 configuration also defaulted presigned URLs to five minutes. A browser or
intermediary could therefore reuse the cached redirect at the end of its
lifetime and receive a target URL that had already expired.

Changing only the default presign lifetime would leave the invariant vulnerable
to an environment override. Removing redirect caching would avoid expiry but
discard the repeated-play and seek latency benefit the stable share endpoint
was designed to provide.

## Decision

The API derives redirect cache lifetime from the configured presigned URL
lifetime:

- reserve a one-minute expiry safety margin;
- cap cache lifetime at five minutes; and
- emit `Cache-Control: no-store` when the presign lifetime cannot provide the
  margin.

With the current five-minute presign default, a video redirect emits
`Cache-Control: public, max-age=240`. A presign lifetime of ten minutes or more
retains the five-minute cache cap. The API assembly passes the same typed S3
configuration used by the presigner into the handler, so the header and signed
URL cannot drift through separate defaults.

## Consequences

A cached redirect always expires before its target URL. Operators may change
`S3_PRESIGNED_URL_TTL` without manually recalculating another setting. Short
lifetimes remain functional but deliberately lose redirect caching.

This supersedes only the historical fixed `max-age=300` value. Stable share
resolution, supersede-chain redirects, 410 removal behavior, and the presigned
URL lifetime itself are unchanged.
