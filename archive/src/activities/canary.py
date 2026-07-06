"""
DOM-selector canary activity — synthetic search that validates X's
tweet markup still matches twitter/session.py's scraper selectors.

Runs hourly via DOMCanaryWorkflow. Issues a known-evergreen search
(e.g. "football goal") with a wide time window and verifies:

  1. The twitter service is reachable and returns success
  2. At least DOM_CANARY_MIN_TWEETS results came back
  3. Each result has a parseable tweet_url containing /status/

A failure here means EITHER:
  - X redesigned the article[data-testid='tweet'] / a[href*='/status/']
    selectors (the alert we care about; surfaces hours of lead time
    versus "where did the goals go?" after a match)
  - The twitter container is unhealthy (auth expired, browser crashed)

Both are alertable. The action name `canary_failed` is distinct from
the actual match-search activity's `service_error` so Grafana can
separate "scraper structural regression" from "transient service
hiccup".

Phase 4 (P4b, 2026-05-26).
"""

import os
from typing import Any, Dict

import requests
from temporalio import activity

from src.utils.footy_logging import log
from src.utils.orchestration_config import (
    DOM_CANARY_MAX_AGE_MINUTES,
    DOM_CANARY_MIN_TWEETS,
    DOM_CANARY_QUERY,
)

MODULE = "canary"

TWITTER_SESSION_URL = os.getenv(
    "TWITTER_SESSION_URL",
    "http://found-footy-prod-twitter:8888",
)


@activity.defn
async def run_dom_canary() -> Dict[str, Any]:
    """Issue a synthetic search and confirm the scraper's selectors still work.

    Returns a dict reporting status, count, and any failure reason.
    The activity NEVER raises — failures are returned as
    `{"passed": False, "reason": "..."}` so the scheduled workflow
    doesn't get into a retry loop. The structured log line is the
    Grafana-visible signal.
    """
    payload = {
        "search_query": DOM_CANARY_QUERY,
        # Wide window — canary tests STRUCTURAL DOM parsing not live
        # freshness. Narrow ages would let the scraper's early-exit
        # ("found old tweet, stopping scroll") bail before we validate
        # the markup. See orchestration_config docstring.
        "max_age_minutes": DOM_CANARY_MAX_AGE_MINUTES,
        "exclude_urls": [],
    }

    log.info(activity.logger, MODULE, "canary_started",
             "DOM canary search started",
             query=DOM_CANARY_QUERY,
             min_tweets=DOM_CANARY_MIN_TWEETS,
             url=TWITTER_SESSION_URL)

    try:
        response = requests.post(
            f"{TWITTER_SESSION_URL}/search",
            json=payload,
            timeout=120,
        )
    except requests.exceptions.ConnectionError as e:
        log.error(activity.logger, MODULE, "canary_unreachable",
                  "Twitter service unreachable from canary",
                  url=TWITTER_SESSION_URL, error=str(e)[:200],
                  error_category="canary",
                  error_class="CanaryServiceUnavailable")
        return {"passed": False, "reason": "twitter_service_unreachable",
                "videos_found": 0}
    except requests.exceptions.Timeout:
        log.error(activity.logger, MODULE, "canary_timeout",
                  "Canary search exceeded 120s timeout",
                  query=DOM_CANARY_QUERY,
                  error_category="canary",
                  error_class="CanaryTimeout")
        return {"passed": False, "reason": "search_timeout",
                "videos_found": 0}
    except Exception as e:
        log.error(activity.logger, MODULE, "canary_error",
                  "Canary search raised unexpectedly",
                  error=str(e)[:200],
                  error_category="canary",
                  error_class=type(e).__name__)
        return {"passed": False, "reason": f"exception_{type(e).__name__}",
                "videos_found": 0}

    # Twitter service responded — check the response shape
    if response.status_code == 503:
        # Service is up but unauthenticated — not a DOM regression but
        # still useful to surface (admin needs to VNC-reauth).
        log.warning(activity.logger, MODULE, "canary_unauth",
                    "Twitter service unauthenticated (503)",
                    status_code=503,
                    error_category="canary",
                    error_class="CanaryUnauthenticated")
        return {"passed": False, "reason": "twitter_unauthenticated",
                "videos_found": 0}

    if response.status_code != 200:
        log.error(activity.logger, MODULE, "canary_service_error",
                  "Twitter service returned non-200",
                  status_code=response.status_code,
                  response=response.text[:200],
                  error_category="canary",
                  error_class="CanaryServiceError")
        return {"passed": False, "reason": f"http_{response.status_code}",
                "videos_found": 0}

    try:
        data = response.json()
    except Exception as e:
        log.error(activity.logger, MODULE, "canary_parse_error",
                  "Could not parse canary response as JSON",
                  error=str(e)[:200],
                  error_category="canary",
                  error_class="CanaryParseError")
        return {"passed": False, "reason": "json_parse_error",
                "videos_found": 0}

    videos = data.get("videos") or []
    count = len(videos)

    # Validate result shape — at least min_tweets, each with a usable URL
    if count < DOM_CANARY_MIN_TWEETS:
        log.error(activity.logger, MODULE, "canary_failed",
                  f"DOM canary returned {count} videos (<{DOM_CANARY_MIN_TWEETS}); "
                  "X markup likely regressed",
                  videos_found=count,
                  min_required=DOM_CANARY_MIN_TWEETS,
                  query=DOM_CANARY_QUERY,
                  error_category="canary",
                  error_class="CanaryBelowThreshold")
        return {"passed": False, "reason": "below_threshold",
                "videos_found": count}

    # Spot-check that the URLs look structurally right (not all "unknown" etc.)
    bad_urls = [
        v.get("tweet_url", "") for v in videos
        if "/status/" not in (v.get("tweet_url") or "")
    ]
    if bad_urls:
        log.error(activity.logger, MODULE, "canary_malformed_urls",
                  f"DOM canary got {len(bad_urls)} malformed URLs out of {count}; "
                  "scraper extracting non-status hrefs",
                  videos_found=count,
                  malformed_count=len(bad_urls),
                  sample_bad_url=bad_urls[0][:80] if bad_urls else "",
                  error_category="canary",
                  error_class="CanaryMalformedURL")
        return {"passed": False, "reason": "malformed_urls",
                "videos_found": count,
                "malformed_count": len(bad_urls)}

    log.info(activity.logger, MODULE, "canary_passed",
             f"DOM canary green: {count} videos with valid /status/ URLs",
             videos_found=count,
             min_required=DOM_CANARY_MIN_TWEETS,
             query=DOM_CANARY_QUERY)
    return {"passed": True, "videos_found": count, "reason": "ok"}
