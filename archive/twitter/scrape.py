"""DOM extraction + URL parsing helpers for Twitter search scraping.

Extracted from session.py in Phase 3 (P3d, 2026-05-26). session.py keeps
the TwitterSessionManager class with browser lifecycle / auth / cookie /
search-orchestration; this module is the per-element analysis layer
called inside the search loop.

Two flavors of helper:

  - **Pure string functions** (extract_tweet_id_from_url,
    extract_username_from_url, is_truncated_snowflake): no Selenium
    dependency, unit-testable in isolation.
  - **DOM walkers** (extract_status_link, extract_tweet_age_minutes,
    extract_tweet_text, extract_video_duration, is_promoted_tweet):
    take a tweet WebElement and pull a single attribute; designed for
    composition inside the main scroll loop in session._do_search.

All helpers return None / empty on failure (the search loop is forgiving
— missing fields skip the tweet rather than crash). DOM helpers swallow
their own selenium exceptions to keep the loop tight; pure helpers raise
nothing (None on parse fail).

Snowflake constants live here too — the same MIN_SNOWFLAKE_LEN guard
that download.py's URL validator uses. Phase 1 surfaced this as an
upstream X-side rendering quirk for deleted/quoted/edge-case tweets.
"""

from datetime import datetime, timezone
from typing import Optional


# Twitter snowflakes have been ≥18 digits since ~early 2020.
# Shorter IDs in the wild are upstream-X rendering quirks for
# deleted / quoted / edge-case tweets that won't syndicate to a
# downloadable video. Same threshold as src/activities/download.py.
MIN_SNOWFLAKE_LEN = 18


# =============================================================================
# Pure string / validation helpers (unit-testable, no DOM)
# =============================================================================


def extract_tweet_id_from_url(tweet_url: str) -> str:
    """Pull the Twitter status ID off a tweet URL.

    Returns "unknown" if the URL doesn't contain /status/ (matches the
    upstream behavior in _do_search — keeping the same sentinel so the
    caller's downstream branches don't change).
    """
    if not tweet_url or "/status/" not in tweet_url:
        return "unknown"
    return tweet_url.split("/status/")[-1].split("?")[0]


def extract_username_from_url(tweet_url: str) -> str:
    """Pull the @username out of a tweet URL.

    Recognized shapes:
      https://x.com/USERNAME/status/12345     → "USERNAME"
      https://twitter.com/USERNAME/status/... → "USERNAME"
      https://x.com/i/status/12345            → "Unknown" (no username path)
    """
    if not tweet_url:
        return "Unknown"
    try:
        url_parts = tweet_url.replace("https://", "").replace("http://", "").split("/")
        if len(url_parts) >= 3 and url_parts[1] != "i":
            return url_parts[1]
    except Exception:
        pass
    return "Unknown"


def is_truncated_snowflake(tweet_id: str) -> bool:
    """True if the ID looks like a truncated snowflake (must be rejected).

    A valid snowflake is at least MIN_SNOWFLAKE_LEN digits. The
    "unknown" sentinel and non-digit IDs are NOT truncated — they're
    handled by callers separately.
    """
    return (
        tweet_id != "unknown"
        and tweet_id.isdigit()
        and len(tweet_id) < MIN_SNOWFLAKE_LEN
    )


# =============================================================================
# DOM walkers (return None / empty on failure; never raise)
# =============================================================================


def extract_status_link(tweet_element) -> Optional[str]:
    """First /status/ href found in the tweet element, or None."""
    try:
        from selenium.webdriver.common.by import By
        status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
        for link in status_links:
            href = link.get_attribute("href")
            if href and "/status/" in href:
                return href
    except Exception:
        pass
    return None


def extract_tweet_age_minutes(tweet_element) -> Optional[float]:
    """Age in minutes from the <time datetime="…"> element. None if missing."""
    try:
        from selenium.webdriver.common.by import By
        time_element = tweet_element.find_element(By.CSS_SELECTOR, "time[datetime]")
        datetime_str = time_element.get_attribute("datetime")
        if not datetime_str:
            return None
        tweet_dt = datetime.fromisoformat(datetime_str.replace("Z", "+00:00"))
        now = datetime.now(timezone.utc)
        return (now - tweet_dt).total_seconds() / 60
    except Exception:
        return None


def is_promoted_tweet(tweet_element) -> bool:
    """True if the tweet element shows a 'Promoted' or 'Ad' indicator."""
    try:
        from selenium.webdriver.common.by import By
        indicators = tweet_element.find_elements(
            By.XPATH,
            ".//*[contains(text(), 'Promoted') or contains(text(), 'Ad')]",
        )
        return bool(indicators)
    except Exception:
        return False


def extract_tweet_text(tweet_element, default: str = "Text not found", max_len: int = 200) -> str:
    """Tweet body text (truncated to max_len). Returns `default` on failure."""
    try:
        from selenium.webdriver.common.by import By
        text_element = tweet_element.find_element(By.CSS_SELECTOR, "[data-testid='tweetText']")
        if text_element:
            return (text_element.text or default).strip()[:max_len]
    except Exception:
        pass
    return default


def _try_parse_duration_text(dur_text: str) -> Optional[float]:
    """Parse "M:SS" or "MM:SS" into seconds; return None on bad shape."""
    if ":" not in dur_text or len(dur_text) > 6:
        return None
    parts = dur_text.split(":")
    if len(parts) != 2:
        return None
    try:
        return int(parts[0]) * 60 + int(parts[1])
    except ValueError:
        return None


def extract_video_duration(tweet_element) -> tuple[bool, Optional[float]]:
    """Find a video element + its duration if present.

    Returns (has_video, duration_seconds_or_None). Tries multiple
    selectors because X's player markup varies by tweet type.
    """
    try:
        from selenium.webdriver.common.by import By
    except Exception:
        return False, None

    has_video = False
    duration_seconds: Optional[float] = None

    for selector in ["video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']"]:
        try:
            video_elements = tweet_element.find_elements(By.CSS_SELECTOR, selector)
            if not video_elements:
                continue
            has_video = True

            # Try the duration attribute first
            try:
                duration_attr = video_elements[0].get_attribute("duration")
                if duration_attr:
                    duration_seconds = float(duration_attr)
            except Exception:
                pass

            # Fall back to scraping the player overlay text
            if duration_seconds is None:
                duration_selectors = [
                    "[aria-label*='Duration']",
                    "[data-testid='videoPlayerDuration']",
                    ".r-1e081e0",
                    "div[dir='ltr'][style*='color']",
                ]
                for dur_selector in duration_selectors:
                    try:
                        for dur_elem in tweet_element.find_elements(By.CSS_SELECTOR, dur_selector):
                            parsed = _try_parse_duration_text((dur_elem.text or "").strip())
                            if parsed is not None:
                                duration_seconds = parsed
                                break
                        if duration_seconds is not None:
                            break
                    except Exception:
                        continue

            break  # found a video element via this selector, stop scanning
        except Exception:
            continue

    return has_video, duration_seconds
