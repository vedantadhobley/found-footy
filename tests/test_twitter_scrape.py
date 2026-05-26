"""Tests for twitter/scrape.py pure helpers (P3d extraction)."""

import os
import sys

# Add project root so we can import the twitter package
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from twitter import scrape


# ─── extract_tweet_id_from_url ───────────────────────────────────────────────


def test_extract_tweet_id_standard_url():
    assert scrape.extract_tweet_id_from_url(
        "https://x.com/CBSSportsGolazo/status/2011864786460409910"
    ) == "2011864786460409910"


def test_extract_tweet_id_i_status_url():
    assert scrape.extract_tweet_id_from_url(
        "https://x.com/i/status/1234567890123456789"
    ) == "1234567890123456789"


def test_extract_tweet_id_strips_query_string():
    assert scrape.extract_tweet_id_from_url(
        "https://x.com/user/status/123456789?s=20"
    ) == "123456789"


def test_extract_tweet_id_no_status_returns_unknown():
    assert scrape.extract_tweet_id_from_url("https://x.com/profile") == "unknown"
    assert scrape.extract_tweet_id_from_url("") == "unknown"


# ─── extract_username_from_url ───────────────────────────────────────────────


def test_extract_username_standard():
    assert scrape.extract_username_from_url(
        "https://x.com/CBSSportsGolazo/status/2011864786460409910"
    ) == "CBSSportsGolazo"


def test_extract_username_twitter_domain():
    assert scrape.extract_username_from_url(
        "https://twitter.com/Liverpool/status/123"
    ) == "Liverpool"


def test_extract_username_i_status_returns_unknown():
    """The /i/status/N shape has no username segment — should return Unknown."""
    assert scrape.extract_username_from_url(
        "https://x.com/i/status/123"
    ) == "Unknown"


def test_extract_username_empty():
    assert scrape.extract_username_from_url("") == "Unknown"


# ─── is_truncated_snowflake ──────────────────────────────────────────────────


def test_truncated_13_digits():
    assert scrape.is_truncated_snowflake("1234567890123") is True


def test_truncated_17_digits():
    assert scrape.is_truncated_snowflake("12345678901234567") is True


def test_valid_18_digit_snowflake():
    assert scrape.is_truncated_snowflake("123456789012345678") is False


def test_valid_19_digit_snowflake():
    assert scrape.is_truncated_snowflake("1234567890123456789") is False


def test_unknown_sentinel_not_truncated():
    """The "unknown" sentinel must NOT be flagged as truncated."""
    assert scrape.is_truncated_snowflake("unknown") is False


def test_non_digit_id_not_truncated():
    """Non-digit IDs (shouldn't happen but defensive) aren't truncated."""
    assert scrape.is_truncated_snowflake("abc123") is False


def test_min_snowflake_len_constant():
    """Constant must match the same threshold used in download.py."""
    assert scrape.MIN_SNOWFLAKE_LEN == 18


# ─── _try_parse_duration_text (private helper, but worth covering) ──────────


def test_duration_parse_seconds_only():
    assert scrape._try_parse_duration_text("0:15") == 15


def test_duration_parse_minutes_seconds():
    assert scrape._try_parse_duration_text("1:30") == 90


def test_duration_parse_two_digit_minutes():
    assert scrape._try_parse_duration_text("12:45") == 765


def test_duration_parse_too_long_returns_none():
    """7-character strings like "100:00" should fail the length filter."""
    assert scrape._try_parse_duration_text("100:000") is None


def test_duration_parse_non_numeric_returns_none():
    assert scrape._try_parse_duration_text("ab:cd") is None


def test_duration_parse_no_colon_returns_none():
    assert scrape._try_parse_duration_text("15") is None
