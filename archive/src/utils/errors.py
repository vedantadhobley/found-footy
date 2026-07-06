"""
Typed error hierarchy for Found Footy activities (Phase 1, 2026-05-26).

Replaces the prior pattern of broad `except Exception: log + return []`
that made transient failures indistinguishable from "no data found".
Each typed error carries:

  - `context: dict` — structured fields for logging (tweet_id, url, etc.)
  - `category: str` — coarse category used by Grafana dashboards
  - `retry_eligible: bool` — class attribute hinting whether activity
    retry is worthwhile (advisory; Temporal's retry policy is still
    declared per-activity)

Activity boundaries SHOULD:

  1. Catch the underlying library exception (httpx, pymongo, botocore).
  2. Classify it into the appropriate typed error here.
  3. Log the typed error's `log_fields()` via `footy_logging`.
  4. Raise the typed error (Temporal serializes it back to the
     workflow as `__cause__` of the ActivityError wrapper).

Generic `except Exception` should be reserved for unknown failure modes,
and always re-raise after logging.

The class hierarchy is shallow on purpose — Grafana dashboards aggregate
on `category`, alert routing on `error_class`, retry policy on
`retry_eligible`. Adding a sub-sub-category is rarely worth the
complexity; just add another peer subclass.

Pickle safety: these are plain Exception subclasses with a `context`
dict attribute; they round-trip cleanly through Temporal's activity
error serialization.
"""

from __future__ import annotations

from typing import Optional


class FootyError(Exception):
    """Base for all Found Footy typed errors. Don't raise directly — use a subclass."""

    category: str = "unknown"
    retry_eligible: bool = False

    def __init__(self, message: str, *, context: Optional[dict] = None):
        super().__init__(message)
        self.context: dict = context or {}

    def log_fields(self) -> dict:
        """Structured fields suitable for `log.error(..., **err.log_fields())`."""
        return {
            "error_category": self.category,
            "error_class": self.__class__.__name__,
            "retry_eligible": self.retry_eligible,
            **self.context,
        }


# ─── Twitter / search-tier errors ────────────────────────────────────────────


class TwitterError(FootyError):
    category = "twitter"


class TwitterRateLimitedError(TwitterError):
    """X / Twitter returned a 429 or equivalent. Back off + retry."""

    retry_eligible = True


class TwitterAuthError(TwitterError):
    """Session cookies expired / login required. Needs human VNC intervention."""

    retry_eligible = False


class TwitterSearchTimeoutError(TwitterError):
    """Twitter search took longer than the activity timeout."""

    retry_eligible = True


class TwitterServiceUnavailableError(TwitterError):
    """The twitter container itself is unreachable (5xx, connection refused)."""

    retry_eligible = True


# ─── Video download errors ───────────────────────────────────────────────────


class VideoDownloadError(FootyError):
    category = "video_download"


class VideoNotAvailableError(VideoDownloadError):
    """Tweet deleted / made private / video removed (404 or equivalent)."""

    retry_eligible = False


class VideoGeoRestrictedError(VideoDownloadError):
    """CDN returned 403 — broadcaster blocked our IP region."""

    retry_eligible = False


class VideoMalformedURLError(VideoDownloadError):
    """Tweet status ID failed format validation (truncated snowflake etc.)."""

    retry_eligible = False


class VideoCDNTimeoutError(VideoDownloadError):
    """CDN connection or read timed out. Worth retrying."""

    retry_eligible = True


class VideoFilteredError(VideoDownloadError):
    """Video downloaded but failed a hard filter (duration, aspect, resolution).

    Not really an "error" — using the error channel keeps the failure-class
    breakdown in Grafana uniform. retry_eligible=False because the source
    won't change between retries.
    """

    retry_eligible = False


# ─── LLM / vision errors ─────────────────────────────────────────────────────


class LLMError(FootyError):
    category = "llm"


class LLMUnavailableError(LLMError):
    """joi llama.cpp server unreachable (connection refused, DNS failure)."""

    retry_eligible = True


class LLMTimeoutError(LLMError):
    """LLM request exceeded our client-side timeout."""

    retry_eligible = True


class LLMValidationError(LLMError):
    """LLM responded but the response shape was malformed / missing fields."""

    retry_eligible = True  # bad sampling; usually resolves on retry


class LLMCapExceededError(LLMError):
    """Too many concurrent LLM calls in-flight (joi's parallel cap is 2)."""

    retry_eligible = True


# ─── MongoDB errors ──────────────────────────────────────────────────────────


class MongoError(FootyError):
    category = "mongo"


class MongoTransientError(MongoError):
    """Network blip, primary stepdown, replication lag. Worth a retry."""

    retry_eligible = True


class MongoConflictError(MongoError):
    """Concurrent write conflict (duplicate key, version mismatch). Caller decides."""

    retry_eligible = False


# ─── S3 / MinIO errors ───────────────────────────────────────────────────────


class S3Error(FootyError):
    category = "s3"


class S3UnavailableError(S3Error):
    """MinIO unreachable or bucket missing."""

    retry_eligible = True


class S3UploadError(S3Error):
    """PUT failed for a specific object. Retry usually works."""

    retry_eligible = True


class S3NotFoundError(S3Error):
    """Object not found at the expected key (only an "error" if we expected it)."""

    retry_eligible = False
