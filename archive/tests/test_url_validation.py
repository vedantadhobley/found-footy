"""Tests for the snowflake-ID URL validation guard added by P2b."""

import asyncio
import os
import sys
import tempfile

import pytest

# Add project root to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.activities.download import download_single_video
from src.utils.errors import VideoMalformedURLError


def _call(url: str, *, event_id: str = "test_evt_1"):
    """Invoke the activity's underlying function bypassing @activity.defn.

    `temporalio.activity.defn` wraps the function but keeps the bare
    callable accessible via `.__wrapped__` (Python's stdlib functools
    convention). For non-Temporal unit tests we just call it directly.
    """
    fn = getattr(download_single_video, "__wrapped__", download_single_video)
    return asyncio.run(
        fn(
            video_url=url,
            video_index=0,
            event_id=event_id,
            temp_dir=tempfile.gettempdir(),
            source_tweet_url=url,
        )
    )


def test_rejects_13_digit_snowflake():
    """The 2026-05-25 Paderborn case — 13-digit IDs are upstream truncation."""
    with pytest.raises(VideoMalformedURLError) as exc_info:
        _call("https://x.com/some_user/status/1234567890123")
    err = exc_info.value
    assert err.context["failure_mode"] == "truncated_snowflake"
    assert err.context["tweet_id_len"] == 13
    assert err.context["min_expected_len"] == 18


def test_rejects_17_digit_snowflake():
    """Edge — even 17 digits is too short to be a modern snowflake."""
    with pytest.raises(VideoMalformedURLError) as exc_info:
        _call("https://x.com/user/status/12345678901234567")
    assert exc_info.value.context["tweet_id_len"] == 17


def test_rejects_url_with_no_status_segment():
    """Pre-guard: URLs missing /status/ should also raise the typed error."""
    with pytest.raises(VideoMalformedURLError) as exc_info:
        _call("https://x.com/user/")
    assert exc_info.value.context["failure_mode"] == "no_status_match"


def test_accepts_valid_19_digit_snowflake_then_fails_at_syndication():
    """An 18+ digit ID passes the local guard.

    The function will then hit the syndication API and (since the tweet
    doesn't actually exist) raise either VideoNotAvailableError (404) or
    a network error. Either is fine — the point is the local guard
    doesn't block valid-shaped IDs.
    """
    from src.utils.errors import VideoDownloadError, TwitterError

    # We don't have network sandboxing here, so accept any of the
    # downstream typed errors. The key guarantee is "NOT a
    # VideoMalformedURLError" because the ID shape is fine.
    with pytest.raises((VideoDownloadError, TwitterError)) as exc_info:
        _call("https://x.com/user/status/1234567890123456789")
    assert not isinstance(exc_info.value, VideoMalformedURLError)
