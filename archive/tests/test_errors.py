"""Tests for src/utils/errors.py — the Phase 1 typed error hierarchy."""

import pickle

import pytest

from src.utils.errors import (
    FootyError,
    LLMError,
    LLMUnavailableError,
    MongoError,
    MongoTransientError,
    S3Error,
    S3UploadError,
    TwitterAuthError,
    TwitterError,
    TwitterRateLimitedError,
    VideoDownloadError,
    VideoGeoRestrictedError,
    VideoMalformedURLError,
    VideoNotAvailableError,
)


def test_base_class_has_default_metadata():
    err = FootyError("oops")
    assert err.category == "unknown"
    assert err.retry_eligible is False
    assert err.context == {}


def test_context_is_stored_and_exposed_in_log_fields():
    err = VideoGeoRestrictedError(
        "403 from cdn.example",
        context={"tweet_id": "12345", "broadcaster": "globo"},
    )
    fields = err.log_fields()
    assert fields["error_category"] == "video_download"
    assert fields["error_class"] == "VideoGeoRestrictedError"
    assert fields["retry_eligible"] is False
    assert fields["tweet_id"] == "12345"
    assert fields["broadcaster"] == "globo"


def test_subclass_overrides_propagate():
    # Twitter rate-limit is retry-eligible
    assert TwitterRateLimitedError("x").retry_eligible is True
    # Twitter auth requires human intervention, not retry
    assert TwitterAuthError("x").retry_eligible is False
    # Geo-restricted is permanently unrecoverable
    assert VideoGeoRestrictedError("x").retry_eligible is False
    # LLM transient errors are retry-eligible
    assert LLMUnavailableError("x").retry_eligible is True


def test_categories_are_distinct_per_family():
    assert TwitterError("x").category == "twitter"
    assert VideoDownloadError("x").category == "video_download"
    assert LLMError("x").category == "llm"
    assert MongoError("x").category == "mongo"
    assert S3Error("x").category == "s3"


def test_isinstance_checks_hold_across_hierarchy():
    err = VideoNotAvailableError("deleted")
    assert isinstance(err, VideoDownloadError)
    assert isinstance(err, FootyError)
    assert isinstance(err, Exception)

    err = MongoTransientError("network")
    assert isinstance(err, MongoError)
    assert isinstance(err, FootyError)


def test_pickle_roundtrip_preserves_context_and_class():
    """Temporal serializes activity errors across the wire — they MUST pickle."""
    err = VideoMalformedURLError(
        "snowflake too short",
        context={"raw_url": "https://x.com/u/status/1234567"},
    )
    raw = pickle.dumps(err)
    restored = pickle.loads(raw)

    assert isinstance(restored, VideoMalformedURLError)
    assert isinstance(restored, VideoDownloadError)
    assert isinstance(restored, FootyError)
    assert restored.context == {"raw_url": "https://x.com/u/status/1234567"}
    assert str(restored) == "snowflake too short"
    assert restored.category == "video_download"
    assert restored.retry_eligible is False


def test_raising_and_catching_typed_error():
    """Validates that callers can catch at any level of the hierarchy."""

    def boom():
        raise S3UploadError("PUT failed", context={"key": "abc"})

    # Catch at the specific class
    with pytest.raises(S3UploadError) as exc_info:
        boom()
    assert exc_info.value.context == {"key": "abc"}

    # Catch at the family base
    with pytest.raises(S3Error):
        boom()

    # Catch at the FootyError root
    with pytest.raises(FootyError):
        boom()


def test_context_defaults_to_empty_dict_not_shared():
    """Make sure we don't have the mutable-default-argument bug."""
    err_a = FootyError("a")
    err_b = FootyError("b")
    err_a.context["mutated"] = True
    # Each instance must have its own dict
    assert err_b.context == {}
