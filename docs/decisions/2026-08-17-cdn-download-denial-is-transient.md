# CDN download denial is transient

## Context

The syndication metadata endpoint and the resolved `video.twimg.com` variant
endpoint can both return HTTP 403, but the responses do not prove the same
condition. Metadata 403 means the tweet is inaccessible from the current
vantage point. A variant-download 403 can instead reflect an expired signed
URL, edge rejection, or authentication requirements. The archived Python
stack and the initial Go adapter classified both as terminal geo-restriction.

That shared classification discarded CDN-denied candidates immediately.
`DownloadAndStage` otherwise has four Temporal attempts, and every attempt
reruns metadata resolution before fetching bytes. A retry can therefore obtain
a refreshed variant URL without adding state or a separate refresh operation.

## Decision

Keep syndication metadata HTTP 403 as terminal `ErrGeoRestricted`. Classify
HTTP 403 from the CDN byte request as transient `ErrCDNForbidden`.
`DownloadAndStage` returns the latter as an activity error, so the existing
four-attempt retry policy reruns the full resolve-and-download unit. If all
attempts fail, FF-002 converts exhaustion into a correlated candidate
`download_error`.

Errors include only the variant host. They do not include the full signed CDN
URL.

## Consequences

A recoverable CDN denial no longer becomes a false geo-restricted rejection.
A persistently inaccessible variant costs up to four resolution and download
attempts before the candidate fails. Metadata-level access denial still fails
fast, so genuinely inaccessible tweets do not consume those retries.

This narrows the historical rebuild-plan contract that treated every
`ErrGeoRestricted` as non-retryable. It does not change other terminal classes,
the download retry count, or the FF-002 failure result.
