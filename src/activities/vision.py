"""
Vision activities — AI clock extraction + soccer/screen validation.

Extracted from download.py in Phase 3 (P3a, 2026-05-26) so the vision-
related code has its own module:

  - Clock-format parsers (parse_clock_field, parse_added_field,
    parse_stoppage_clock_field, compute_absolute_minute, validate_timestamp,
    plus the legacy parse_broadcast_clock kept for the test harness)
  - Frame extraction helper (_extract_frame_for_vision)
  - LLM call helper (_call_vision_model)
  - validate_video_is_soccer Temporal activity

NOTE on the log `module` field — we keep `MODULE = "download"` inside
these functions so existing Grafana dashboards and the Phase 1 query
catalog in docs/logging.md continue to filter the same logical pipeline
stage. The filename is documentation-of-organization; the MODULE
constant is documentation-of-identity. They can differ.

Module split is back-compat: src/activities/download.py re-exports
every symbol here so callers using `from src.activities import download
as download_activities` continue to work unchanged.

Phase 5 of the roadmap may replace _call_vision_model + the
SOCCER/SCREEN parts of validate_video_is_soccer with embedding-based
classification, leaving only OCR (CLOCK / ADDED / STOPPAGE_CLOCK) on
the chat VL model. The clock parsers below would still be relevant.
"""

from typing import Any, Dict, Optional
import asyncio
import json
import os
import re

from temporalio import activity

from src.utils.config import LLAMA_CHAT_URL
from src.utils.errors import (
    LLMTimeoutError,
    LLMUnavailableError,
    LLMValidationError,
)
from src.utils.footy_logging import log
from src.utils.orchestration_config import LLM_CONCURRENCY_PER_WORKER

# Log identity kept as "download" for dashboard continuity (see module docstring).
MODULE = "download"

# Semaphore to limit concurrent LLM requests per worker process.
# joi llama-chat runs --parallel 4 with 2 workers, so allow 2 concurrent per worker.
_LLM_SEMAPHORE = asyncio.Semaphore(LLM_CONCURRENCY_PER_WORKER)


# =============================================================================
# Clock parsers
# =============================================================================


def parse_broadcast_clock(raw_clock: str | None) -> int | None:
    """
    Parse raw broadcast clock text into absolute match minute.

    Broadcast clocks appear in 5 distinct format categories:

    A) Running match clock (most common):
       "34:12" → 34, "84:28" → 84, "112:54" → 112

    B) Period indicator + time (relative or absolute):
       "2H 5:00" → 50 (relative: 45+5)
       "2H 67:00" → 67 (absolute: 67 >= 45)
       "ET 04:04" → 94 (relative: 90+4)
       "ET 102:53" → 102 (absolute: 102 >= 90)

    C) Compact stoppage (base + added minutes):
       "45+2" → 47, "45+2:30" → 47, "90+3" → 93

    D) Broadcast stoppage display (frozen base + allocated time + sub-clock):
       "45:00 +2 00:43" → 45 (base=45, sub-clock 0 mins elapsed)
       "90:00 +4 02:17" → 92 (base=90, sub-clock 2 mins elapsed)
       "90:00 +5 03:45" → 93 (base=90, sub-clock 3 mins elapsed)
       The main clock freezes at the period boundary (45:00, 90:00).
       +N is the total allocated stoppage time.
       MM:SS after that is a running sub-clock within the stoppage.
       Actual minute = base_minutes + sub-clock_minutes.

    E) Base time + stoppage (no space, colon in base):
       "90:00+3:15" → 93, "45:00+2" → 47

    Smart detection for relative vs absolute with period indicators:
    - ET + minutes >= 90 → already absolute (don't add 90)
    - ET + minutes <= 30 → relative to ET start (add 90)
    - 2H + minutes >= 45 → already absolute (don't add 45)
    - 2H + minutes < 45 → relative to 2H start (add 45)

    NOTE: With the structured prompt, Patterns D and E are handled by
    separate ADDED/STOPPAGE_CLOCK fields. This function is kept as a
    fallback for the combined CLOCK field parsing and exercised by the
    legacy test_clock_parsing.py harness.

    Args:
        raw_clock: Raw text from AI vision model (e.g., "45:00 +2 00:43")

    Returns:
        Absolute match minute (0-120+), or None if unparseable
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None

    text = raw_clock.upper().strip()

    # Detect period indicators (but don't apply offset yet)
    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))

    # Remove indicators for time parsing
    clean_text = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()

    # =========================================================================
    # Pattern D: Broadcast stoppage display with running sub-clock
    # "45:00 +2 00:43" → base=45, sub-clock elapsed=0 min → 45
    # "90:00 +4 02:17" → base=90, sub-clock elapsed=2 min → 92
    # =========================================================================
    broadcast_stoppage = re.match(
        r'(\d{1,3}):(\d{2})\s*\+\s*\d+\s+(\d{1,2}):(\d{2})',
        clean_text
    )
    if broadcast_stoppage:
        base_min = int(broadcast_stoppage.group(1))
        elapsed_added_min = int(broadcast_stoppage.group(3))
        return base_min + elapsed_added_min

    # =========================================================================
    # Pattern E: Base time with stoppage (no space, colon in base)
    # "90:00+3:15" → base=90, added=3 → 93
    # "45:00+2" → base=45, added=2 → 47
    # =========================================================================
    base_stoppage = re.match(
        r'(\d{1,3}):(\d{2})\+(\d+)(?::(\d{2}))?$',
        clean_text
    )
    if base_stoppage:
        base_min = int(base_stoppage.group(1))
        added_min = int(base_stoppage.group(3))
        return base_min + added_min

    # =========================================================================
    # Pattern C: Compact stoppage "45+2:30" or "90+3" or "45+2"
    # =========================================================================
    stoppage_match = re.match(r'(\d+)\s*\+\s*(\d+)', clean_text)
    if stoppage_match:
        base_min = int(stoppage_match.group(1))
        added_min = int(stoppage_match.group(2))
        return base_min + added_min

    # =========================================================================
    # Patterns A & B: Standard MM:SS or just MM (with optional period offset)
    # =========================================================================
    time_match = re.search(r'(\d{1,3}):(\d{2})', clean_text)
    if not time_match:
        just_minutes = re.match(r'^(\d{1,3})$', clean_text.strip())
        if just_minutes:
            minutes = int(just_minutes.group(1))
        else:
            return None  # Can't parse
    else:
        minutes = int(time_match.group(1))

    # SMART OFFSET LOGIC: Determine if time is relative or absolute
    if has_et:
        if minutes >= 90:
            return minutes  # Already absolute
        elif minutes <= 30:
            return 90 + minutes  # Relative to ET start
        else:
            return minutes  # Ambiguous (31-89), assume absolute

    elif has_2h:
        if minutes >= 45:
            return minutes  # Already absolute
        else:
            return 45 + minutes  # Relative to 2H start

    elif has_1h:
        return minutes  # First half, no offset needed

    else:
        # No indicator — assume absolute (most common)
        return minutes


# =============================================================================
# Structured Clock Field Parsers (used by validate_video_is_soccer)
# =============================================================================


def parse_clock_field(raw_clock: str | None) -> int | None:
    """
    Parse the CLOCK field from structured prompt → absolute minute.

    Unlike parse_broadcast_clock(), this only handles the primary timer
    since ADDED and STOPPAGE_CLOCK are separate fields. Patterns D & E
    are eliminated — only A, B, C remain.

    Args:
        raw_clock: Raw CLOCK field text (e.g., "34:12", "2H 15:30", "45+2")

    Returns:
        Absolute match minute, or None if unparseable
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    text = raw_clock.upper().strip()

    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    clean = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()

    # Pattern C: compact stoppage "45+2", "90+3"
    sm = re.match(r'(\d+)\s*\+\s*(\d+)', clean)
    if sm:
        return int(sm.group(1)) + int(sm.group(2))

    # Patterns A & B: MM:SS or just MM
    tm = re.search(r'(\d{1,3}):(\d{2})', clean)
    if not tm:
        jm = re.match(r'^(\d{1,3})$', clean.strip())
        if jm:
            minutes = int(jm.group(1))
        else:
            return None
    else:
        minutes = int(tm.group(1))

    # Smart offset logic
    if has_et:
        return (90 + minutes) if minutes <= 30 else minutes
    elif has_2h:
        return (45 + minutes) if minutes < 45 else minutes
    elif has_1h:
        return minutes
    return minutes


def parse_added_field(raw_added: str | None) -> int | None:
    """
    Parse the ADDED field: "+4" → 4, "+6" → 6.

    Args:
        raw_added: Raw ADDED field text (e.g., "+4", "+6")

    Returns:
        Added time minutes, or None if not visible
    """
    if not raw_added or raw_added.upper().strip() in ("NONE", "N/A"):
        return None
    m = re.search(r'\+\s*(\d+)', raw_added)
    return int(m.group(1)) if m else None


def parse_stoppage_clock_field(raw_stoppage: str | None) -> int | None:
    """
    Parse the STOPPAGE_CLOCK field: "03:57" → 3.

    This is the sub-timer that counts elapsed time within stoppage.
    We only need the minutes component.

    Args:
        raw_stoppage: Raw STOPPAGE_CLOCK field text (e.g., "03:57", "02:17")

    Returns:
        Elapsed stoppage minutes, or None if not visible
    """
    if not raw_stoppage or raw_stoppage.upper().strip() in ("NONE", "N/A"):
        return None
    m = re.match(r'(\d{1,2}):(\d{2})', raw_stoppage.strip())
    return int(m.group(1)) if m else None


def compute_absolute_minute(clock_min: int | None, stoppage_min: int | None) -> int | None:
    """
    Compute absolute minute from structured clock fields.

    If stoppage_clock is present, the main clock is frozen at a period
    boundary and the real time is clock_min + stoppage_min.

    Args:
        clock_min: Parsed CLOCK field (absolute minute)
        stoppage_min: Parsed STOPPAGE_CLOCK field (elapsed stoppage minutes)

    Returns:
        Absolute match minute, or None if no clock data
    """
    if clock_min is None:
        return None
    if stoppage_min is not None:
        return clock_min + stoppage_min
    return clock_min


def validate_timestamp(
    frames: list[dict],
    api_elapsed: int,
    api_extra: int | None
) -> tuple[bool, int | None, str]:
    """
    Check if EITHER extracted clock matches the API-reported event time.

    Accepts structured frame data (dicts with raw_clock, raw_stoppage_clock)
    from the 5-field prompt. Uses per-field parsers for clean extraction.

    Two-phase comparison:
    1. Direct match: parsed clock minute within ±1 of expected
    2. Stoppage-time OCR correction: if the vision model dropped the leading
       digit (e.g., read "92:36" as "02:36"), try rebasing with api_elapsed

    Args:
        frames: List of dicts with raw_clock, raw_added, raw_stoppage_clock keys
                (one per frame, typically 25% and 75%)
        api_elapsed: API elapsed minute (e.g., 90 for stoppage, 30 for regular)
        api_extra: API extra/stoppage minutes (e.g., 3), or None

    Returns:
        Tuple of:
        - clock_verified: True if at least one clock matches
        - extracted_minute: The matching minute (or best guess), None if no clock visible
        - timestamp_status: "verified", "unverified", or "rejected"
    """
    # Guard: if no API time available (e.g., in-flight replay with default=0),
    # we can't validate — default to unverified
    if not api_elapsed:
        return (False, None, "unverified")

    # API reports the minute AFTER the goal happened
    expected = api_elapsed + (api_extra or 0) - 1

    # Parse all frames using structured field parsers
    computed_minutes = []
    for frame in frames:
        clock_min = parse_clock_field(frame.get("raw_clock"))
        stoppage_min = parse_stoppage_clock_field(frame.get("raw_stoppage_clock"))
        absolute = compute_absolute_minute(clock_min, stoppage_min)
        if absolute is not None:
            computed_minutes.append(absolute)

    if not computed_minutes:
        return (False, None, "unverified")

    # Phase 1: Direct match — computed minute within ±1 of expected
    for minute in computed_minutes:
        if abs(minute - expected) <= 1:
            return (True, minute, "verified")

    # Phase 2: Stoppage-time OCR correction
    # During stoppage, vision models sometimes drop the leading digit:
    #   Screen: "92:36 +8" → AI reads: "02:36 +8" → parser returns: 2
    # Since api_elapsed IS the dropped base, we can recover:
    #   corrected = api_elapsed + parsed = 90 + 2 = 92
    if api_extra is not None:
        for minute in computed_minutes:
            corrected = api_elapsed + minute
            if abs(corrected - expected) <= 1:
                return (True, corrected, "verified")

    # Clock visible but wrong time → rejected; return closest for logging
    closest = min(computed_minutes, key=lambda m: abs(m - expected))
    return (False, closest, "rejected")


# =============================================================================
# Frame extraction + LLM client
# =============================================================================


def _extract_frame_for_vision(file_path: str, timestamp: float) -> Optional[str]:
    """
    Extract a single frame from video and return as base64.

    Args:
        file_path: Path to video file
        timestamp: Timestamp in seconds to extract frame

    Returns:
        Base64-encoded PNG image or None if extraction fails
    """
    import subprocess
    import base64

    try:
        cmd = [
            "ffmpeg",
            "-ss", str(timestamp),
            "-i", file_path,
            "-vframes", "1",
            "-f", "image2pipe",
            "-vcodec", "png",
            "-"
        ]
        result = subprocess.run(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10
        )

        if result.returncode != 0 or not result.stdout:
            log.warning(activity.logger, MODULE, "frame_extraction_failed",
                        "Frame extraction failed",
                        timestamp=timestamp, returncode=result.returncode)
            return None

        return base64.b64encode(result.stdout).decode('utf-8')
    except subprocess.TimeoutExpired:
        log.warning(activity.logger, MODULE, "frame_extraction_timeout",
                    "Frame extraction timed out", timestamp=timestamp)
        return None
    except Exception as e:
        log.warning(activity.logger, MODULE, "frame_extraction_error",
                    "Frame extraction error", timestamp=timestamp, error=str(e))
        return None


async def _call_vision_model(image_base64: str, prompt: str) -> Optional[Dict[str, Any]]:
    """
    Call vision LLM with an image using llama.cpp OpenAI-compatible API.

    Uses a semaphore to limit concurrent requests per worker (llama.cpp is
    single-slot for inference). Retries with backoff on transient failures.

    Args:
        image_base64: Base64-encoded image
        prompt: Question to ask about the image

    Returns:
        Dict with response content or None if failed
    """
    import httpx

    llama_url = LLAMA_CHAT_URL
    max_retries = 3

    payload = {
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:image/jpeg;base64,{image_base64}"
                        }
                    }
                ]
            }
        ],
        "max_tokens": 100,
        "temperature": 0.1,
        "chat_template_kwargs": {"enable_thinking": False},
        "response_format": {"type": "json_object"}
    }

    for attempt in range(1, max_retries + 1):
        try:
            async with _LLM_SEMAPHORE:
                log.debug(activity.logger, MODULE, "vision_call",
                          "Calling vision model", url=llama_url, attempt=attempt)

                async with httpx.AsyncClient(timeout=60.0) as client:
                    response = await client.post(
                        f"{llama_url}/v1/chat/completions",
                        json=payload,
                    )

                    if response.status_code == 200:
                        return response.json()
                    else:
                        # joi returns 503 when --parallel cap is hit; tag it
                        # distinctly in the log.
                        if response.status_code == 503:
                            log.warning(activity.logger, MODULE, "vision_cap_exceeded",
                                        "Vision model 503 (parallel cap)",
                                        url=llama_url, attempt=attempt,
                                        error_category="llm",
                                        error_class="LLMCapExceededError")
                        else:
                            log.warning(activity.logger, MODULE, "vision_http_error",
                                        "Vision model returned error",
                                        status_code=response.status_code, attempt=attempt,
                                        error_category="llm",
                                        error_class="LLMValidationError")
                        if attempt < max_retries:
                            await asyncio.sleep(2 * attempt)
                            continue
                        # Exhausted retries: return None so the caller (which
                        # handles None as "vision unavailable, skip this video")
                        # gets the same contract it had before.
                        return None
        except httpx.ConnectError as e:
            err = LLMUnavailableError(
                f"Cannot connect to LLM at {llama_url}: {e}",
                context={"url": llama_url, "attempt": attempt, "error_detail": str(e)[:200]},
            )
            log.error(activity.logger, MODULE, "vision_connect_failed",
                      "Cannot connect to LLM", **err.log_fields())
            raise err from e
        except (httpx.TimeoutException, httpx.ReadError) as e:
            error_type = type(e).__name__
            if attempt < max_retries:
                wait = 3 * attempt
                log.warning(activity.logger, MODULE, "vision_retry",
                            f"Vision model {error_type}, retrying in {wait}s",
                            attempt=attempt, max_retries=max_retries, error_type=error_type)
                await asyncio.sleep(wait)
                continue
            err = LLMTimeoutError(
                f"Vision model {error_type} after {max_retries} attempts",
                context={
                    "url": llama_url,
                    "underlying_error_type": error_type,
                    "error_detail": str(e)[:200],
                },
            )
            log.warning(activity.logger, MODULE, "vision_timeout",
                        f"Vision model {error_type} after {max_retries} attempts",
                        **err.log_fields())
            raise err from e
        except Exception as e:
            err = LLMValidationError(
                f"Vision model unexpected error: {str(e)[:120]}",
                context={
                    "url": llama_url,
                    "underlying_error_type": type(e).__name__,
                    "error_detail": str(e)[:200],
                },
            )
            log.error(activity.logger, MODULE, "vision_error",
                      "Vision model error", **err.log_fields())
            raise err from e

    return None


# =============================================================================
# Temporal activity
# =============================================================================


@activity.defn
async def validate_video_is_soccer(
    file_path: str,
    event_id: str,
    event_minute: int = 0,
    event_extra: int | None = None,
) -> Dict[str, Any]:
    """
    AI validation to check if video is soccer AND not a phone-TV recording.
    Also extracts broadcast clock data for timestamp verification.

    Uses vision LLM to analyze frames and determine:
    1. Is this a soccer/football video?
    2. Is this someone filming a TV screen with their phone?
    3. What does the broadcast clock show? (structured: CLOCK/ADDED/STOPPAGE_CLOCK)

    Timestamp verification is folded into is_valid:
    - is_valid = is_soccer AND not screen_recording AND timestamp not rejected

    Args:
        file_path: Local path to video file
        event_id: Event ID for logging
        event_minute: API elapsed minute (e.g., 45, 90) — default 0 means no validation
        event_extra: API extra/stoppage minutes (e.g., 3), or None

    Returns:
        Dict with:
        - is_valid: True if soccer AND not screen AND timestamp not rejected
        - is_soccer, is_screen_recording, confidence, reason, checks_performed
        - clock_verified: True if extracted clock matches API time ±1
        - extracted_minute: Best extracted clock minute (None if no clock)
        - timestamp_status: "verified" / "unverified" / "rejected"
        - extracted_clocks: Raw structured data per frame

    Raises:
        FileNotFoundError: If video file doesn't exist
    """
    # Verify file exists before validation
    if not os.path.exists(file_path):
        log.error(activity.logger, MODULE, "validate_file_missing",
                  "Video file not found for validation",
                  event_id=event_id, file_path=file_path)
        raise FileNotFoundError(f"Video file not found for validation: {file_path}")

    # Get video duration for frame extraction
    duration = 0.0
    try:
        import subprocess
        probe_cmd = [
            "ffprobe", "-v", "error", "-show_entries",
            "format=duration", "-of", "csv=p=0", file_path
        ]
        result = subprocess.run(probe_cmd, capture_output=True, text=True, timeout=10)
        if result.returncode == 0 and result.stdout.strip():
            duration = float(result.stdout.strip())
    except Exception as e:
        log.warning(activity.logger, MODULE, "get_duration_failed",
                    "Failed to get duration", error=str(e))
        duration = 10.0  # Assume 10s if can't read

    if duration < 1.0:
        # Too short to analyze
        return {
            "is_valid": True,
            "confidence": 0.5,
            "reason": "Video too short to analyze",
            "detected_features": [],
            "checks_performed": 0,
        }

    # Combined vision prompt for soccer detection, phone-TV rejection, AND structured clock extraction
    # Uses JSON output format for reliable parsing across models
    prompt = """/no_think
Analyze this image. Return ONLY a JSON object with these fields:

{"soccer": true/false, "screen": true/false, "clock": "MM:SS or null", "added": "+N or null", "stoppage_clock": "MM:SS or null"}

soccer: true if this is from a live soccer/football match broadcast or highlight (players on pitch, match action, goals, replays, celebrations, VAR footage, stadium recordings). false if studio/podcast, press conference, ads, other sports, or just graphics with no match footage.

screen: true ONLY if someone is filming a TV with their phone (moiré patterns, visible TV bezel, screen glare, tilted angle, visible room/furniture). false for professional broadcasts, overlays, scoreboards, watermarks, letterbox bars. When in doubt, false.

clock: The primary match timer text exactly as shown (e.g. "34:12", "90:00"), or null if not visible.

added: Additional time indicator near the clock (e.g. "+4", "+6"), or null if none.

stoppage_clock: A separate smaller sub-timer for stoppage time (e.g. "03:57"), or null if none."""

    log.info(activity.logger, MODULE, "validate_started",
             "Starting AI vision validation",
             event_id=event_id, duration=round(duration, 1),
             file=os.path.basename(file_path))

    def parse_response(resp) -> dict:
        """Parse vision model JSON response into structured dict."""
        result = {"is_soccer": False, "is_screen": False, "raw_clock": None,
                  "raw_added": None, "raw_stoppage_clock": None}

        if not resp:
            return result

        content = ""
        if "choices" in resp:
            content = resp.get("choices", [{}])[0].get("message", {}).get("content", "")
        elif "response" in resp:
            content = resp.get("response", "")

        if not content:
            return result

        # Strip markdown code fences if present
        stripped = content.strip()
        if stripped.startswith("```"):
            stripped = re.sub(r'^```(?:json)?\s*', '', stripped)
            stripped = re.sub(r'```\s*$', '', stripped).strip()

        # Try JSON parsing first (primary path)
        try:
            parsed = json.loads(stripped)
            result["is_soccer"] = bool(parsed.get("soccer", False))
            result["is_screen"] = bool(parsed.get("screen", False))
            result["raw_clock"] = parsed.get("clock")
            result["raw_added"] = parsed.get("added")
            result["raw_stoppage_clock"] = parsed.get("stoppage_clock")
            return result
        except (json.JSONDecodeError, TypeError):
            pass

        # Fallback: text parsing for non-JSON responses
        content = content.replace("*", "")
        content_upper = content.upper()

        if "SKIP" in content_upper:
            result["is_soccer"] = True
            return result

        is_soccer = "SOCCER:YES" in content_upper or "SOCCER: YES" in content_upper
        if "SOCCER:" not in content_upper:
            is_soccer = "YES" in content_upper and "SOCCER" in content_upper
        result["is_soccer"] = is_soccer

        is_screen = "SCREEN:YES" in content_upper or "SCREEN: YES" in content_upper
        if "SCREEN:" not in content_upper:
            is_screen = "MOIRE" in content_upper or "BEZEL" in content_upper or "TV FRAME" in content_upper
        result["is_screen"] = is_screen

        clock_match = re.search(r'CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
        if clock_match:
            clock_value = clock_match.group(1).strip()
            if clock_value.upper() not in ("NONE", "N/A", "NOT VISIBLE", "NO CLOCK"):
                result["raw_clock"] = clock_value

        added_match = re.search(r'ADDED:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
        if added_match:
            added_value = added_match.group(1).strip()
            if added_value.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
                result["raw_added"] = added_value

        stoppage_match = re.search(r'STOPPAGE_CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
        if stoppage_match:
            stoppage_value = stoppage_match.group(1).strip()
            if stoppage_value.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
                result["raw_stoppage_clock"] = stoppage_value

        return result

    # Extract frames at 25% and 75%
    t_25 = duration * 0.25
    t_75 = duration * 0.75

    frame_25 = _extract_frame_for_vision(file_path, t_25)
    frame_75 = _extract_frame_for_vision(file_path, t_75)

    if not frame_25 and not frame_75:
        log.error(activity.logger, MODULE, "frame_extraction_failed",
                  "Failed to extract ANY frames",
                  event_id=event_id, file=file_path)
        raise RuntimeError("Failed to extract frames for validation")

    # =========================================================================
    # Smart 2-3 check strategy with heartbeats:
    # 1. Check 25% and 75% first
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks)
    # =========================================================================
    checks_performed = 0
    frame_25_data, frame_75_data, frame_50_data = None, None, None
    soccer_25, screen_25 = None, None
    soccer_75, screen_75 = None, None

    if frame_25:
        activity.heartbeat(f"AI vision check 1/2 (25% frame)...")
        response_25 = await _call_vision_model(frame_25, prompt)
        checks_performed += 1
        parsed_25 = parse_response(response_25)
        soccer_25 = parsed_25["is_soccer"]
        screen_25 = parsed_25["is_screen"]
        frame_25_data = parsed_25
        log.info(activity.logger, MODULE, "vision_check",
                 "Vision check completed",
                 check_pct=25, is_soccer=soccer_25, is_screen=screen_25,
                 clock=parsed_25["raw_clock"], added=parsed_25["raw_added"],
                 stoppage=parsed_25["raw_stoppage_clock"])

    # Heartbeat before second check
    activity.heartbeat(f"AI vision check 2/2 (75% frame)...")

    # Check 75%
    if frame_75:
        response_75 = await _call_vision_model(frame_75, prompt)
        checks_performed += 1
        parsed_75 = parse_response(response_75)
        soccer_75 = parsed_75["is_soccer"]
        screen_75 = parsed_75["is_screen"]
        frame_75_data = parsed_75
        log.info(activity.logger, MODULE, "vision_check",
                 "Vision check completed",
                 check_pct=75, is_soccer=soccer_75, is_screen=screen_75,
                 clock=parsed_75["raw_clock"], added=parsed_75["raw_added"],
                 stoppage=parsed_75["raw_stoppage_clock"])

    # =========================================================================
    # Determine BOTH soccer and screen results using same tiebreaker logic
    # =========================================================================
    soccer_disagree = soccer_25 is not None and soccer_75 is not None and soccer_25 != soccer_75
    screen_disagree = screen_25 is not None and screen_75 is not None and screen_25 != screen_75
    need_tiebreaker = soccer_disagree or screen_disagree

    soccer_50, screen_50 = None, None

    if need_tiebreaker:
        log.info(activity.logger, MODULE, "vision_tiebreaker",
                 "Disagreement detected, checking 50% tiebreaker")
        activity.heartbeat("AI vision tiebreaker (50% frame)...")

        t_50 = duration * 0.50
        frame_50 = _extract_frame_for_vision(file_path, t_50)

        if frame_50:
            response_50 = await _call_vision_model(frame_50, prompt)
            checks_performed += 1
            parsed_50 = parse_response(response_50)
            soccer_50 = parsed_50["is_soccer"]
            screen_50 = parsed_50["is_screen"]
            frame_50_data = parsed_50  # Extracted but NOT used for timestamp validation
            log.info(activity.logger, MODULE, "vision_check",
                     "Vision tiebreaker completed",
                     check_pct=50, is_soccer=soccer_50, is_screen=screen_50,
                     clock=parsed_50["raw_clock"])

    # Determine soccer result
    if soccer_25 is None and soccer_75 is not None:
        is_soccer = soccer_75
        confidence = 0.7
        soccer_reason = "Single frame check (25% failed)"
    elif soccer_75 is None and soccer_25 is not None:
        is_soccer = soccer_25
        confidence = 0.7
        soccer_reason = "Single frame check (75% failed)"
    elif soccer_25 == soccer_75:
        is_soccer = soccer_25
        confidence = 0.95 if is_soccer else 0.90
        soccer_reason = f"Both checks agree: {'soccer' if is_soccer else 'not soccer'}"
    else:
        # Disagreement - use tiebreaker (2/3 majority)
        if soccer_50 is not None:
            yes_votes = sum([soccer_25 or False, soccer_50, soccer_75 or False])
            is_soccer = yes_votes >= 2
            confidence = 0.85
            soccer_reason = f"Tiebreaker decided: {yes_votes}/3 votes for soccer"
        else:
            is_soccer = soccer_25 if soccer_25 is not None else False
            confidence = 0.6
            soccer_reason = "Tiebreaker failed, using first check"

    # Determine screen recording result (same tiebreaker logic)
    if screen_25 is None and screen_75 is not None:
        is_screen_recording = screen_75
    elif screen_75 is None and screen_25 is not None:
        is_screen_recording = screen_25
    elif screen_25 == screen_75:
        is_screen_recording = screen_25 or False
    else:
        # Disagreement - use tiebreaker (2/3 majority to REJECT)
        if screen_50 is not None:
            screen_votes = sum([screen_25 or False, screen_50, screen_75 or False])
            is_screen_recording = screen_votes >= 2  # Need 2/3 to reject
        else:
            # Tiebreaker failed, default to NOT rejecting
            is_screen_recording = False

    # Final validation: must be soccer AND not a screen recording
    is_valid = is_soccer and not is_screen_recording

    # Collect structured clock data from validation frames (25% and 75% only)
    validation_frames = []
    if frame_25_data:
        validation_frames.append(frame_25_data)
    if frame_75_data:
        validation_frames.append(frame_75_data)

    # Timestamp validation — only if we have API minute data
    clock_verified = False
    extracted_minute = None
    timestamp_status = "unverified"

    if event_minute and validation_frames:
        clock_verified, extracted_minute, timestamp_status = validate_timestamp(
            validation_frames, event_minute, event_extra
        )

        # Fold timestamp rejection into is_valid
        if timestamp_status == "rejected":
            is_valid = False

    # Build extracted_clocks for logging/debugging
    extracted_clocks = []
    for frame_data in [frame_25_data, frame_75_data]:
        if frame_data:
            extracted_clocks.append({
                "clock": frame_data.get("raw_clock"),
                "added": frame_data.get("raw_added"),
                "stoppage_clock": frame_data.get("raw_stoppage_clock"),
            })
    if frame_50_data:
        extracted_clocks.append({
            "clock": frame_50_data.get("raw_clock"),
            "added": frame_50_data.get("raw_added"),
            "stoppage_clock": frame_50_data.get("raw_stoppage_clock"),
        })

    if is_screen_recording:
        reason = "Rejected: phone recording of TV/screen detected"
        log.info(activity.logger, MODULE, "validate_rejected",
                 "Video rejected: phone-TV recording",
                 rejection_type="screen_recording",
                 event_id=event_id, checks=checks_performed, is_screen=True,
                 clocks=extracted_clocks)
    elif not is_soccer:
        reason = soccer_reason
        log.info(activity.logger, MODULE, "validate_rejected",
                 "Video rejected: not soccer",
                 rejection_type="not_soccer",
                 event_id=event_id, checks=checks_performed, reason=soccer_reason,
                 clocks=extracted_clocks)
    elif timestamp_status == "rejected":
        reason = f"Rejected: wrong game minute (expected ~{event_minute + (event_extra or 0) - 1}, got {extracted_minute})"
        log.info(activity.logger, MODULE, "validate_rejected",
                 "Video rejected: wrong timestamp",
                 rejection_type="wrong_timestamp",
                 event_id=event_id, checks=checks_performed,
                 expected=event_minute + (event_extra or 0) - 1,
                 extracted=extracted_minute, clocks=extracted_clocks)
    else:
        reason = soccer_reason
        log.info(activity.logger, MODULE, "validate_passed",
                 "Video passed validation",
                 event_id=event_id, checks=checks_performed,
                 confidence=round(confidence, 2),
                 clock_verified=clock_verified,
                 extracted_minute=extracted_minute,
                 timestamp_status=timestamp_status,
                 clocks=extracted_clocks)

    return {
        "is_valid": is_valid,
        "confidence": confidence,
        "reason": reason,
        "is_soccer": is_soccer,
        "is_screen_recording": is_screen_recording,
        "detected_features": ["soccer_field"] if is_soccer else [],
        "checks_performed": checks_performed,
        # Structured clock extraction data
        "clock_verified": clock_verified,
        "extracted_minute": extracted_minute,
        "timestamp_status": timestamp_status,
        "extracted_clocks": extracted_clocks,
    }
