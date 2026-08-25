// errors.go — the typed error taxonomy for video resolution + download.
// Activities errors.Is against these to classify a candidate's failure
// (deleted / geo-blocked / rate-limited / CDN-forbidden / CDN-timeout / malformed URL /
// truncated snowflake) and record the right outcome_class, rather than
// treating every failure as a generic retryable blip. Mirrors Python's
// VideoNotAvailableError / VideoGeoRestrictedError / VideoCDNTimeoutError /
// VideoMalformedURLError classes (archive/src/activities/download.py).
package syndication

import "errors"

var (
	// ErrMalformedTweetURL — the URL has no /status/<id> segment, so we
	// can't derive a tweet id. Permanent; do not retry.
	ErrMalformedTweetURL = errors.New("syndication: malformed tweet URL (no /status/<id>)")

	// ErrTruncatedSnowflake — the extracted id is shorter than a real
	// Twitter snowflake (≥18 digits since ~2020). Almost always an
	// upstream truncation bug (the 2026-05-25 Paderborn-Wolfsburg
	// post-mortem). Reject early so it surfaces as truncated_snowflake
	// rather than a generic 404 at the syndication API. Permanent.
	ErrTruncatedSnowflake = errors.New("syndication: truncated snowflake id (<18 digits)")

	// ErrVideoNotAvailable — syndication 404: tweet deleted or private.
	// Permanent for this candidate.
	ErrVideoNotAvailable = errors.New("syndication: video not available (deleted or private)")

	// ErrGeoRestricted — 403 from the syndication metadata endpoint: the
	// tweet is inaccessible from our vantage point. Permanent for this
	// candidate (a different vantage might succeed, but retrying from here
	// is not expected to change the result).
	ErrGeoRestricted = errors.New("syndication: video geo-restricted (403)")

	// ErrCDNForbidden — 403 while fetching a resolved variant from the CDN.
	// Transient: retrying DownloadAndStage resolves the tweet again, which
	// can refresh an expired or edge-rejected variant URL.
	ErrCDNForbidden = errors.New("syndication: CDN denied video download (403)")

	// ErrRateLimited — 429. Transient; the caller may back off + retry.
	ErrRateLimited = errors.New("syndication: rate limited (429)")

	// ErrCDNTimeout — the syndication API or the CDN didn't respond in
	// time. Transient; retryable.
	ErrCDNTimeout = errors.New("syndication: CDN/API timeout")

	// ErrTransport — a non-timeout HTTP transport failure while resolving
	// metadata or fetching video bytes. Transient; retryable.
	ErrTransport = errors.New("syndication: HTTP transport failure")

	// ErrInvalidResponse — the upstream returned an unexpected status or a
	// successful response whose JSON could not be decoded. Transient because
	// edge/backend failures can produce either shape.
	ErrInvalidResponse = errors.New("syndication: invalid upstream response")

	// ErrCDNStream — the selected video response started but failed while its
	// body was copied. The caller retries the full resolve-and-download unit.
	ErrCDNStream = errors.New("syndication: CDN response stream failed")

	// ErrNoVideoVariants — the tweet-result parsed, but carried no usable
	// mp4 video variant (e.g. an image-only tweet, or amplify content we
	// can't reach cookieless). Permanent for this candidate.
	ErrNoVideoVariants = errors.New("syndication: no usable mp4 video variant")
)
