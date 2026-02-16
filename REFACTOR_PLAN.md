# Refactor: Validation & Deduplication Overhaul

**Branch:** `refactor/valid-deduplication`  
**Parent:** `feature/grafana-logging`  
**Started:** 2026-02-13

## Current Status

| Phase | Status | Progress |
|-------|--------|----------|
| Phase 1: AI Clock Extraction | ✅ Complete | 10/10 tasks — deployed at `4dcf3bc` |
| Phase 2: Verification-Scoped Dedup | ⬜ Not Started | 0/6 tasks done |

**Phase 1 — Deployed (2026-02-16):**
- ✅ Structured 5-field vision prompt (SOCCER / SCREEN / CLOCK / ADDED / STOPPAGE_CLOCK)
- ✅ `parse_response()` returns dict with all 5 fields
- ✅ Per-field parsers: `parse_clock_field()`, `parse_added_field()`, `parse_stoppage_clock_field()`, `compute_absolute_minute()`
- ✅ `validate_timestamp()` accepts structured frame dicts, returns `(bool, int|None, str)`
- ✅ `validate_video_is_soccer()` takes `event_minute`/`event_extra`, folds timestamp rejection into `is_valid`
- ✅ `event_minute`/`event_extra` wired through entire chain: monitor → twitter → download → validate
- ✅ Verification fields (`clock_verified`, `extracted_minute`, `timestamp_verified`) attached to `video_info` and passed to MongoDB
- ✅ `VideoFields` expanded (6 → 17 constants), `S3Video` updated, `DownloadStats` has `timestamp_rejected`
- ✅ 58 unit tests passing (updated for structured dicts + 4 new test classes)
- ✅ Workers rebuilt and running (`docker compose up -d --build worker`)

**Phase 2 — Next:**
1. Scope `deduplicate_videos()` by `timestamp_verified` — verified vs verified, unverified vs unverified
2. Validate Phase 1 data quality from today's live games before implementing Phase 2

---

## Problem Statement

The current pipeline has two major quality issues:

1. **Deduplication is unreliable** — perceptual hash matching produces false positives (e.g. the Szoboszlai meme replacing a real goal clip) and the "longer = better" replacement logic makes it worse. Dedup needs to be **disabled** until we have a reliable verification layer.

2. **No timestamp verification** — we accept any video that "looks like soccer" but never verify it's showing the *right moment* in the match. A 30th-minute goal clip and a 75th-minute goal clip from the same match are indistinguishable to the current pipeline.

---

## Phase 1: AI Timestamp Extraction (NEW VERIFICATION LAYER)

### Concept

Broadcast soccer footage almost always displays a game clock (e.g. `30:00`, `47:12`). We can extract this timestamp from the frames we're already sending to the vision model, then compare it to the API-reported event time.

### How the game clock works

- Game clock format: `MM:SS` (e.g. `30:00`, `45:00`)
- Added time continues counting: `45+2` in the API = `47:xx` on the broadcast clock
- **The API reports the minute AFTER the goal happened.** So API time `elapsed=45, extra=2` (which we calculate as minute 47) means the goal actually occurred at minute **46:xx** in the broadcast clock.

### What we have from the API

From `event.time`:
- `elapsed`: int — The base minute (e.g. `45`)
- `extra`: int | None — Additional time (e.g. `2`)

The "broadcast minute" = `elapsed + (extra or 0)` — this is the minute as it would appear on the game clock, except the API reports +1 from when the goal actually happened.

So: **expected broadcast minute = elapsed + (extra or 0) - 1**

### Current AI validation flow

```
validate_video_is_soccer(file_path, event_id)
├── Extract frame at 25% of video duration
├── Extract frame at 75% of video duration  
├── Call vision LLM on each frame with prompt:
│   "Is this SOCCER? Is this a SCREEN recording?"
├── If 25% and 75% disagree → extract 50% frame as tiebreaker
└── Return: {is_valid, is_soccer, is_screen_recording, confidence}
```

Currently the function takes only `file_path` and `event_id`. The event minute is not passed down.

### Proposed change: Add timestamp extraction to vision calls

The 25% and 75% frames are the key ones for timestamp extraction — they represent early and late points in the video. The game clock should be visible in most broadcast frames.

**CRITICAL:** We do NOT ask the AI to parse/convert the time. Broadcast clock formats are wildly inconsistent, and we need deterministic parsing logic. Instead, we ask the AI to report **exactly what it sees** on screen, and WE parse it.

### Broadcast clock format categories

Broadcast clocks fall into 5 distinct format categories. Our parser must handle all of them.

#### Category A: Running match clock (most common)

The main clock runs continuously from 0:00. No period indicator. This is the simplest format — the minutes value IS the absolute minute.

| Seen on screen | Parsed minute |
|---------------|---------------|
| `34:12` | 34 |
| `84:28` | 84 |
| `112:54` | 112 |

#### Category B: Period indicator + time

Some broadcasts prefix the clock with a period indicator (`2H`, `1H`, `ET`, `AET`). The time shown may be **relative** to the period start or **absolute** from match start — we detect which using thresholds.

| Seen on screen | Relative or absolute? | Parsed minute |
|---------------|----------------------|---------------|
| `1H 35:00` | Relative (but no offset needed) | 35 |
| `2H 5:00` | Relative (5 < 45, add 45) | 50 |
| `2H 67:00` | Absolute (67 >= 45, don't add) | 67 |
| `ET 04:04` | Relative (4 <= 30, add 90) | 94 |
| `AET 04:04` | Same as ET | 94 |
| `ET 15:00` | Relative (15 <= 30, add 90) | 105 |
| `ET 102:53` | Absolute (102 >= 90, don't add) | 102 |

#### Category C: Compact stoppage time

The base period minute and added minutes are shown with a `+` separator. No colon before the `+`. The base is always explicit (45, 90, 105) so the result is always `base + added`.

| Seen on screen | Parsed minute |
|---------------|---------------|
| `45+2` | 47 |
| `45+2:30` | 47 (`:30` is seconds, ignored) |
| `90+3` | 93 |
| `90+3:15` | 93 |
| `105+2` | 107 |

#### Category D: Broadcast stoppage display (frozen base + sub-clock)

**This is the format seen in our Tolisso test: `45:00 +2 00:43`**

Many broadcasts freeze the main clock at the period boundary (45:00 or 90:00) and display a separate sub-clock counting elapsed stoppage time. The format is:

```
BASE:SS  +TOTAL_ALLOCATED  ELAPSED_MM:SS
45:00    +2                00:43
```

- `45:00` = main clock frozen at end of regulation half
- `+2` = referee allocated 2 minutes of stoppage
- `00:43` = 43 seconds of the 2 minutes have elapsed so far

**The actual match minute = BASE_MINUTES + ELAPSED_MINUTES from the sub-clock.**

| Seen on screen | Base | Sub-clock elapsed | Parsed minute |
|---------------|------|-------------------|---------------|
| `45:00 +2 00:43` | 45 | 0 min 43s → 0 | 45 |
| `45:00 +3 01:30` | 45 | 1 min 30s → 1 | 46 |
| `90:00 +4 02:17` | 90 | 2 min 17s → 2 | 92 |
| `90:00 +5 03:45` | 90 | 3 min 45s → 3 | 93 |
| `90:00 +6 05:12` | 90 | 5 min 12s → 5 | 95 |

**Why this matters:** Without parsing the sub-clock, `90:00 +4 02:17` would be parsed as minute 90 (from the `90:00`). If the API says the goal was at minute 92, that's a diff of 2 — outside our ±1 tolerance → **false timestamp rejection**.

#### Category E: Base time + stoppage (no space, colon in base)

Similar to C but the base includes `:SS`. No space between the base time and the `+`.

| Seen on screen | Parsed minute |
|---------------|---------------|
| `90:00+3:15` | 93 |
| `45:00+2` | 47 |

#### Non-parseable values

| Seen on screen | Parsed |
|---------------|--------|
| `HT` | None (half time) |
| `FT` | None (full time) |
| `NONE` | None (AI: no clock visible) |
| `None` / empty | None |

**Key insight:** The AI should report the RAW text it sees. Our code parses it.

### Structured Extraction Approach (RECOMMENDED)

**Discovery (2026-02-16):** Testing on 7 real production videos (13+ frames across stoppage time and regular time goals) revealed that the vision model can reliably decompose broadcast scoreboards into **three separate visual elements**. This structured extraction is significantly more robust than asking for a single combined clock string.

#### Why three fields instead of one

The single `CLOCK` field approach asked the AI to concatenate multiple visual elements into one string (e.g. `"90:00 +4 03:57"`), which created two problems:
1. **Inconsistent formatting** — the AI might join elements with spaces, no spaces, different orderings, or omit parts entirely
2. **Complex regex parsing** — we needed 5 distinct pattern categories (D→E→C→A/B) with priority ordering to handle every possible concatenation

The structured approach eliminates both problems by asking for each element separately.

#### The three fields

Broadcast scoreboards display up to three distinct time elements:

| Field | Visual Element | What It Shows | Example Values |
|-------|---------------|---------------|----------------|
| **CLOCK** | Primary match timer | Running time or frozen at period boundary | `48:50`, `96:46`, `90:00` |
| **ADDED** | Stoppage allocation badge | Total stoppage minutes allocated by referee | `+4`, `+6`, or `NONE` |
| **STOPPAGE_CLOCK** | Stoppage sub-timer | Running elapsed time within stoppage period | `03:57`, `1:30`, or `NONE` |

#### Three broadcast types and how they decompose

| Broadcast Type | CLOCK | ADDED | STOPPAGE_CLOCK | How to Compute Absolute Minute |
|----------------|-------|-------|----------------|-------------------------------|
| **Regular time** | Running (e.g. `48:50`) | NONE | NONE | `clock_minutes` directly |
| **Running-clock stoppage** | Continues past boundary (e.g. `96:46`) | Present (e.g. `+6`) | NONE | `clock_minutes` directly |
| **Frozen-clock stoppage** | Frozen at boundary (e.g. `90:00`) | Present (e.g. `+4`) | Running (e.g. `03:57`) | `clock_minutes + stoppage_minutes` |

The ADDED field is never needed for minute computation — it's a **cross-check signal** against `api_extra`.

#### Evidence from live testing

Tested across production videos with the structured prompt. Results were 100% accurate:

**Stoppage time goals (all correctly show ADDED):**
| Goal | Frame | CLOCK | ADDED | STOPPAGE_CLOCK | Computed |
|------|-------|-------|-------|----------------|----------|
| Fofana (Lens) 90+5 | 10% | 90:00 | +4 | 03:57 | 93 |
| Fofana (Lens) 90+5 | 25% | 90:00 | +4 | 03:57 | 93 |
| Fofana (Lens) 90+5 | 50% | 90:00 | +4 | 03:57 | 93 |
| Fofana (Lens) 90+5 | 75% | 90:00 | +4 | 04:01 | 94 |
| Fofana (Lens) 90+5 | 90% | 90:00 | +4 | 04:03 | 94 |
| Sarr (Palace) 45+2 | 25% | 46:19 | +4' | — | 46 |
| Sarr (Palace) 45+2 | 75% | 46:29 | +4 | — | 46 |
| Havertz (Arsenal) 90+7 | 25% | 96:46 | +6 | — | 96 |
| Havertz (Arsenal) 90+7 | 75% | 97:09 | +6 | — | 97 |
| Moreira (Porto) 90+4 | 25% | 90:00 | +4 | 3:11 | 93 |
| Moreira (Porto) 90+4 | 75% | 90:00 | +4 | 3:11 | 93 |
| Williams (Bilbao) 90+6 | 25% | 95:24 | +7 | — | 95 |
| Williams (Bilbao) 90+6 | 75% | 95:28 | +7 | — | 95 |

**Regular time goals (all correctly report NONE):**
| Goal | Frame | CLOCK | ADDED | STOPPAGE_CLOCK |
|------|-------|-------|-------|----------------|
| Vini Jr (Real Madrid) 15' | 25% | 15:27 | NONE | NONE |
| Vini Jr (Real Madrid) 15' | 75% | 15:27 | NONE | NONE |
| de Frutos (Leganés) 49' | 25% | 48:50 | NONE | NONE |
| de Frutos (Leganés) 49' | 75% | 48:50 | NONE | NONE |
| Alvarez (City) 65' | 25% | 64:36 | NONE | NONE |
| Alvarez (City) 65' | 75% | 64:36 | NONE | NONE |

#### How this simplifies parsing

**Before (single CLOCK):** 5 regex patterns in priority order, handling every possible concatenation:
- Pattern D: `(\d+):(\d{2})\s*\+\s*\d+\s+(\d+):(\d{2})` (broadcast stoppage display)
- Pattern E: `(\d+):(\d{2})\+(\d+)(?::(\d{2}))?` (base+stoppage no space)
- Pattern C: `(\d+)\s*\+\s*(\d+)` (compact stoppage)
- Patterns A/B: `(\d+):(\d{2})` with period indicator logic

**After (structured fields):** Each field parsed independently with trivial logic:
- **CLOCK:** Only needs Patterns A/B (plain MM:SS with optional period indicator). Pattern C kept as fallback for compact broadcasts. **Patterns D and E are eliminated entirely.**
- **ADDED:** `re.search(r'\+\s*(\d+)', value)` — just extract the number
- **STOPPAGE_CLOCK:** `re.match(r'(\d+):(\d{2})', value)` — just extract minutes

#### Updated prompt

```
3. CLOCK: What does the PRIMARY match timer show?
   Report the main clock display (e.g., "34:12", "90:00", "2H 15:30"). Copy exactly.

4. ADDED: Is there an ADDITIONAL TIME indicator (like "+3", "+5") shown near the clock?
   If yes, report exactly what you see (e.g., "+4", "+6").
   If none visible, answer NONE.

5. STOPPAGE_CLOCK: Is there a SEPARATE smaller clock counting time within added/stoppage time?
   Some broadcasts freeze the main clock (e.g., at 90:00) and show a small running
   sub-timer (e.g., "03:57") for the elapsed stoppage time.
   If you see this separate sub-timer, report it (e.g., "03:57").
   If there is no separate sub-timer, answer NONE.

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO
CLOCK: <exact text from main timer> or NONE
ADDED: <exact indicator like +4> or NONE
STOPPAGE_CLOCK: <exact sub-timer text> or NONE
```

#### Updated `parse_response()` return

Currently returns 3-tuple: `(is_soccer, is_screen, raw_clock)`

**New return:** dict: `{is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock}`

Each raw value is the string exactly as reported by the AI, or `None` if the AI said NONE or the field was missing.

### Clock parsing logic (structured extraction)

With the structured three-field prompt, parsing becomes significantly simpler. Each field is parsed independently.

#### Parsing CLOCK (primary timer)

The CLOCK field now only contains the primary match timer — no stoppage concatenations. This means **Patterns D and E are eliminated**. Only Patterns A, B, and C (as fallback) are needed.

```python
def parse_clock_field(raw_clock: str | None) -> int | None:
    """
    Parse the CLOCK field from structured extraction.
    
    Handles:
    A) Running match clock: "34:12" → 34, "84:28" → 84
    B) Period indicator + time: "2H 5:00" → 50, "ET 04:04" → 94
    C) Compact stoppage (fallback): "45+2" → 47 (if AI puts it all in CLOCK)
    
    Patterns D and E are eliminated — the structured prompt separates
    the frozen base, allocation indicator, and sub-clock into their own fields.
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    
    text = raw_clock.upper().strip()
    
    # Detect period indicators
    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    clean_text = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()
    
    # Pattern C fallback: compact stoppage "45+2:30" or "90+3"
    # (in case AI dumps the combined display into CLOCK despite structured prompt)
    stoppage_match = re.match(r'(\d+)\s*\+\s*(\d+)', clean_text)
    if stoppage_match:
        return int(stoppage_match.group(1)) + int(stoppage_match.group(2))
    
    # Patterns A & B: standard MM:SS with optional period offset
    time_match = re.search(r'(\d{1,3}):(\d{2})', clean_text)
    if not time_match:
        just_minutes = re.match(r'^(\d{1,3})$', clean_text.strip())
        if just_minutes:
            minutes = int(just_minutes.group(1))
        else:
            return None
    else:
        minutes = int(time_match.group(1))
    
    # Period indicator offset logic (unchanged from current implementation)
    if has_et:
        return (90 + minutes) if minutes <= 30 else minutes
    elif has_2h:
        return (45 + minutes) if minutes < 45 else minutes
    elif has_1h:
        return minutes
    else:
        return minutes
```

#### Parsing ADDED (stoppage indicator)

```python
def parse_added_field(raw_added: str | None) -> int | None:
    """Parse the ADDED field: "+4" → 4, "+6" → 6, "NONE" → None."""
    if not raw_added or raw_added.upper().strip() == "NONE":
        return None
    match = re.search(r'\+\s*(\d+)', raw_added)
    return int(match.group(1)) if match else None
```

#### Parsing STOPPAGE_CLOCK (sub-timer)

```python
def parse_stoppage_clock_field(raw_stoppage: str | None) -> int | None:
    """Parse the STOPPAGE_CLOCK field: "03:57" → 3, "1:30" → 1, "NONE" → None."""
    if not raw_stoppage or raw_stoppage.upper().strip() == "NONE":
        return None
    match = re.match(r'(\d{1,2}):(\d{2})', raw_stoppage.strip())
    return int(match.group(1)) if match else None
```

#### Computing absolute minute

```python
def compute_absolute_minute(
    clock_minutes: int | None,
    stoppage_clock_minutes: int | None
) -> int | None:
    """
    Compute absolute match minute from structured extraction fields.
    
    Three cases:
    1. Frozen-clock stoppage (STOPPAGE_CLOCK present):
       clock_minutes + stoppage_clock_minutes
       e.g., CLOCK=90:00, STOPPAGE_CLOCK=03:57 → 90 + 3 = 93
    
    2. Running-clock stoppage (ADDED present, no STOPPAGE_CLOCK):
       clock_minutes directly (already includes stoppage)
       e.g., CLOCK=96:46, ADDED=+6 → 96
    
    3. Regular time (neither ADDED nor STOPPAGE_CLOCK):
       clock_minutes directly
       e.g., CLOCK=48:50 → 48
    
    Note: Cases 2 and 3 have the same formula — the ADDED field is only
    used for cross-checking against api_extra, not for minute computation.
    """
    if clock_minutes is None:
        return None
    if stoppage_clock_minutes is not None:
        return clock_minutes + stoppage_clock_minutes
    return clock_minutes
```

#### Backward compatibility: `parse_broadcast_clock()`

The existing `parse_broadcast_clock()` function (with all 5 patterns) is **kept as a fallback**. If the AI returns a single combined string in the CLOCK field despite the structured prompt, we can still parse it. The function remains unchanged — it just becomes the second-choice parser rather than the primary one.

### Timestamp validation logic

The API gives us two fields per event: `elapsed` (int) and `extra` (int | null).

**Regular time goals:** `elapsed` = actual minute, `extra` = null.
Example: goal at 30th minute → `{"elapsed": 30, "extra": null}`

**Stoppage time goals:** `elapsed` = period boundary (always 45, 90, 105, or 120), `extra` = minutes into stoppage.
Example: 90+3 goal → `{"elapsed": 90, "extra": 3}`

This means `elapsed + extra` = the reported minute of the goal. Since the API reports the minute AFTER the event, the broadcast clock should show approximately `elapsed + extra - 1`.

**Two-phase comparison (unchanged):**

1. **Direct match** — Does the computed absolute minute fall within ±1 of `expected`?
2. **Stoppage-time OCR correction** — Vision models sometimes drop the leading digit of the clock (reading `92:36` as `02:36`). In stoppage time, `api_elapsed` IS the dropped base (90), so we can try: `corrected = api_elapsed + parsed_minute`. The ±1 comparison naturally constrains which values can match — only parsed minutes ≈ `api_extra` (±1) will pass.

**New: ADDED cross-check (soft signal)**

The structured extraction gives us the ADDED field, which we can compare against `api_extra`:
- If AI sees ADDED (e.g., `+4`) AND `api_extra` is not null → confirms this is stoppage time. The values may differ slightly (referee can extend stoppage beyond the announced allocation).
- If AI sees ADDED but `api_extra` is null → soft warning. Could be a regular-time goal near period end where stoppage indicator is already displayed.
- If AI sees no ADDED but `api_extra` is set → soft warning. Could be running-clock broadcast that doesn't show the indicator separately.

This cross-check is **informational only** — it does NOT affect the verified/unverified outcome. The minute comparison remains the sole determinant.

**Why the OCR correction is safe:**
- Only triggers when `api_extra is not None` (confirmed stoppage time)
- The math `abs(api_elapsed + minute - expected) <= 1` simplifies to `abs(minute - api_extra + 1) <= 1`, meaning only parsed minutes in the range `[api_extra - 2, api_extra]` can match
- For `api_extra=3`: only parsed minutes 1, 2, or 3 pass
- For `api_extra=8`: only parsed minutes 6, 7, or 8 pass
- No arbitrary threshold needed — the comparison constrains naturally

```python
def validate_timestamp(
    extracted_clocks: list[dict],       # Each: {clock, added, stoppage_clock}
    api_elapsed: int,
    api_extra: int | None
) -> tuple[bool, int | None, str]:
    """
    Check if EITHER extracted frame matches the API-reported event time.
    
    Input: list of structured extraction results, one per frame.
    Each dict has: clock (str|None), added (str|None), stoppage_clock (str|None)
    
    Two-phase comparison:
    1. Direct match: computed absolute minute within ±1 of expected
    2. Stoppage-time OCR correction: if the vision model dropped the leading
       digit (e.g., read "92:36" as "02:36"), try rebasing with api_elapsed
    """
    # Guard: if no API time available (e.g., in-flight Temporal replay with
    # default event_minute=0), we can't validate — treat as unverified.
    if not api_elapsed:
        return (False, None, "unverified")
    
    # API reports the minute AFTER the goal happened
    expected = api_elapsed + (api_extra or 0) - 1
    
    # Parse all frames
    computed_minutes = []
    for frame in extracted_clocks:
        clock_min = parse_clock_field(frame.get("clock"))
        stoppage_min = parse_stoppage_clock_field(frame.get("stoppage_clock"))
        absolute = compute_absolute_minute(clock_min, stoppage_min)
        if absolute is not None:
            computed_minutes.append(absolute)
        
        # Log ADDED cross-check (informational, doesn't affect verification)
        added_val = parse_added_field(frame.get("added"))
        if added_val is not None and api_extra is None:
            logger.info("ADDED cross-check: AI sees +%d but API has no extra", added_val)
        elif added_val is None and api_extra is not None:
            logger.info("ADDED cross-check: AI sees no indicator but API has extra=%d", api_extra)
    
    if not computed_minutes:
        return (False, None, "unverified")
    
    # Phase 1: Direct match — computed minute within ±1 of expected
    for minute in computed_minutes:
        if abs(minute - expected) <= 1:
            return (True, minute, "verified")
    
    # Phase 2: Stoppage-time OCR correction
    # Same logic as before — if the AI dropped the leading digit of the
    # frozen clock (read "90:00" as "0:00"), try rebasing
    if api_extra is not None:
        for minute in computed_minutes:
            corrected = api_elapsed + minute
            if abs(corrected - expected) <= 1:
                return (True, corrected, "verified")
    
    # Clock visible but wrong time → rejected entirely (not stored)
    closest = min(computed_minutes, key=lambda m: abs(m - expected))
    return (False, closest, "rejected")
```

### Test cases for clock parsing

#### CLOCK field parsing (parse_clock_field)

| Raw CLOCK text | Parsed minute | Category | Notes |
|----------------|---------------|----------|-------|
| `"34:12"` | 34 | A | Standard first half |
| `"84:28"` | 84 | A | Standard second half |
| `"112:54"` | 112 | A | Standard extra time |
| `"2H 5:00"` | 50 | B | 45 + 5 (relative, 5 < 45) |
| `"2H 15:30"` | 60 | B | 45 + 15 (relative, 15 < 45) |
| `"2H 67:00"` | 67 | B | Already absolute (67 >= 45, don't add) |
| `"1H 35:00"` | 35 | B | Explicit first half |
| `"ET 04:04"` | 94 | B | 90 + 4 (relative, 4 < 30) |
| `"AET 04:04"` | 94 | B | 90 + 4 (same as ET) |
| `"ET 15:00"` | 105 | B | 90 + 15 (relative, 15 < 30) |
| `"ET 102:53"` | 102 | B | Already absolute (102 >= 90, don't add) |
| `"ET 95:00"` | 95 | B | Already absolute (95 >= 90, don't add) |
| `"45+2:30"` | 47 | C (fallback) | Compact stoppage — should rarely appear with structured prompt |
| `"45+2"` | 47 | C (fallback) | Compact stoppage |
| `"90+3:15"` | 93 | C (fallback) | Compact stoppage |
| `"90+3"` | 93 | C (fallback) | Compact stoppage |
| `"105+2"` | 107 | C (fallback) | Compact ET stoppage |
| `"HT"` | None | — | Half time, no clock |
| `"FT"` | None | — | Full time, no clock |
| `None` | None | — | No clock visible |
| `"NONE"` | None | — | AI reported no clock |

#### ADDED field parsing (parse_added_field)

| Raw ADDED text | Parsed value | Notes |
|----------------|-------------|-------|
| `"+4"` | 4 | Standard |
| `"+6"` | 6 | Standard |
| `"+4'"` | 4 | With trailing apostrophe (seen in Sarr test) |
| `"+ 3"` | 3 | With space |
| `"NONE"` | None | No indicator visible |
| `None` | None | Field missing |

#### STOPPAGE_CLOCK field parsing (parse_stoppage_clock_field)

| Raw STOPPAGE_CLOCK text | Parsed minutes | Notes |
|------------------------|---------------|-------|
| `"03:57"` | 3 | Standard MM:SS |
| `"1:30"` | 1 | Single-digit minutes |
| `"00:43"` | 0 | Less than 1 minute |
| `"NONE"` | None | No sub-timer visible |
| `None` | None | Field missing |

#### Absolute minute computation (compute_absolute_minute)

| CLOCK parsed | STOPPAGE_CLOCK parsed | Absolute | Broadcast type |
|-------------|----------------------|----------|----------------|
| 48 | None | 48 | Regular time |
| 96 | None | 96 | Running-clock stoppage |
| 90 | 3 | 93 | Frozen-clock stoppage |
| 45 | 0 | 45 | Frozen-clock stoppage (just started) |
| 90 | 5 | 95 | Frozen-clock stoppage |
| None | None | None | No clock visible |

### What passes, what fails (validation)

| API time | Expected | Frame 25% | Frame 75% | Result |
|----------|----------|-----------|-----------|--------|
| 45+2 | 46 | CLOCK=45:30, ADDED=+4 | CLOCK=47:12, ADDED=+4 | **PASS** (45 within ±1 of 46) |
| 95 | 94 | CLOCK=ET 04:04, ADDED=NONE | CLOCK=ET 05:30, ADDED=NONE | **PASS** (94 matches) |
| 50 | 49 | CLOCK=2H 5:00 | CLOCK=2H 6:30 | **PASS** (50 within ±1 of 49) |
| 30 | 29 | CLOCK=65:00 | CLOCK=72:00 | **FAIL** (both clocks are wrong half) |
| 30 | 29 | CLOCK=NONE | CLOCK=28:45 | **PASS** (28 within ±1 of 29) |
| 30 | 29 | CLOCK=NONE | CLOCK=NONE | **UNVERIFIED** (no clock visible) |
| 30 | 29 | CLOCK=HT | CLOCK=HT | **UNVERIFIED** (HT = no usable clock) |
| 90+4 | 93 | CLOCK=90:00, ADDED=+4, STOP=02:17 | CLOCK=NONE | **PASS** (90+2=92, within ±1 of 93) |
| 45+1 | 45 | CLOCK=45:00, ADDED=+2, STOP=00:43 | same | **PASS** (45+0=45, matches 45) |
| 0 | — | CLOCK=34:12 | CLOCK=35:30 | **UNVERIFIED** (api_elapsed=0 guard) |
| 90+3 | 92 | CLOCK=92:07, ADDED=+8 | CLOCK=02:36, ADDED=+8 | **PASS** (92:07→92; 02:36 OCR correction: 90+2=92) |
| 45+2 | 46 | CLOCK=01:15, ADDED=+3 | CLOCK=NONE | **PASS** (01:15 OCR correction: 45+1=46) |

### When no clock is visible

If neither frame has a parseable game clock, we **cannot verify** the timestamp. The video is marked `timestamp_verified: false` and kept as an **unverified** video.

**We fail open** — many valid clips crop out the scoreboard (close-up celebrations, replays, fan recordings). We don't reject them, but we keep them separate from verified clips during deduplication. Unverified videos can only dedup against other unverified videos for the same event.

### Data flow changes (ALL COMPLETE — commit `4dcf3bc`)

1. ✅ **`TwitterWorkflowInput`** — `event_minute: int = 0` and `event_extra: Optional[int] = None` added
2. ✅ **`DownloadWorkflow.run()`** — `event_minute: int = 0` and `event_extra: int | None = None` added (positional with defaults)
3. ✅ **`validate_video_is_soccer()`** — `event_minute: int = 0` and `event_extra: int = None` params added
4. ✅ **`monitor_workflow.py`** — passes `event_minute=minute, event_extra=extra` to `TwitterWorkflowInput`
5. ✅ **Vision prompt** — structured 5-field prompt (SOCCER / SCREEN / CLOCK / ADDED / STOPPAGE_CLOCK)
6. ✅ **Parse response** — returns dict: `{is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock}`
7. ✅ **Parse clock fields** — `parse_clock_field()`, `parse_added_field()`, `parse_stoppage_clock_field()`, `compute_absolute_minute()` per frame
8. ✅ **Validate timestamp** — `validate_timestamp()` with structured frame dicts + API time
9. ✅ **Return value** — `clock_verified`, `extracted_minute`, `timestamp_status` in return dict. `is_valid` absorbs timestamp rejection.

---

## Phase 2: Timestamp-Scoped Deduplication

### The problem with current dedup

The current perceptual hash dedup is **event-scoped** — it only compares videos within the same `event_id`. But it has no way to distinguish a clip of the actual goal from a clip of a different moment in the same match (e.g. a 30th-minute highlight vs the 75th-minute goal we actually want). Hash similarity between two broadcast clips of the same match is high regardless of which minute they show.

### The verification model

Timestamp extraction from Phase 1 classifies each video into one of three outcomes:

| Status | Criteria | What happens |
|--------|----------|--------------|
| **Verified** | Extracted clock minute is within ±1 of expected API time `(elapsed + extra - 1)` | Kept. Deduplicated only against other verified videos. |
| **Rejected** | Extracted clock minute exists but is **outside** the ±1 range | **Discarded entirely** — this is a clip of the wrong part of the match. Never uploaded, never stored. |
| **Unverified** | No clock visible in either the 25% or 75% frame | Kept. Deduplicated only against other unverified videos. Cannot verify, but may still be valid (cropped scoreboard, close-up replays, fan recordings). |

This is a **massive improvement** over the current approach because:
1. Rejected clips (wrong minute) are discarded outright — they previously polluted dedup clusters and could replace correct clips
2. Verified and unverified videos are deduplicated independently — a verified 30th-minute clip can never match against an unverified clip that might be from minute 75
3. S3 dedup gets the same benefit — when comparing against existing S3 videos, only compare within the same verification group

### How this applies to both dedup phases

#### Batch dedup (`deduplicate_videos` Phase 1)

Currently: all downloaded videos in a single event are compared against each other using perceptual hashes. Perceptually similar videos are grouped into clusters, and the best video from each cluster survives (best = longest, or highest resolution if durations are similar). There can be many clusters — one survivor per cluster, not one overall winner.

**New behavior:**
1. Rejected videos were already discarded in the DownloadWorkflow (before reaching dedup)
2. Separate remaining videos into verified (`timestamp_verified=True`) and unverified (`timestamp_verified=False`)
3. Run perceptual hash clustering **only within verified** and **only within unverified** separately
4. A verified video is never compared against an unverified video — they can't end up in the same cluster
5. All survivors from both groups proceed to S3 dedup

#### S3 dedup (`deduplicate_videos` Phase 2)

Currently: each cluster survivor is compared against all existing S3 videos for the event using perceptual hashes.

**New behavior:**
1. Each S3 video in MongoDB has a stored `timestamp_verified` field (true, false, or absent for legacy videos)
2. Each survivor is only compared against S3 videos **in the same verification group**
3. A verified video is only compared to existing verified S3 videos
4. An unverified video is only compared to existing unverified S3 videos
5. Legacy S3 videos (no `timestamp_verified` field) are treated as unverified

This prevents the exact scenario that caused the Szoboszlai bug: a meme video (unverified or rejected) replacing a verified goal clip.

### Storage architecture

**Where things are stored:**
- **MongoDB** (`events` collection, `_s3_videos` array): All video **metadata** including `timestamp_verified`, `extracted_minute`, `perceptual_hash`, resolution, etc. This is the queryable source of truth.
- **S3** (`footy-videos` bucket): The actual video **file bytes**. Just blob storage.

The `S3Video` TypedDict (confusingly named) defines the schema for what's stored in **MongoDB**. The name is historical — it describes videos that have been uploaded to S3, but the metadata itself lives in Mongo.

### Storage rules

| Status | Stored in MongoDB? | Uploaded to S3? | Notes |
|--------|-------------------|-----------------|-------|
| **Verified** (clock matches) | ✅ Yes | ✅ Yes | Full storage, queryable |
| **Unverified** (no clock) | ✅ Yes | ✅ Yes | Full storage, queryable |
| **Rejected** (wrong clock) | ❌ No | ❌ No | Discarded — not stored anywhere |

### MongoDB schema changes

The video object stored in MongoDB `_s3_videos` array currently has:
```
{url, perceptual_hash, resolution_score, file_size, popularity, rank}
```

**Add two new fields:**
```
{
  url, perceptual_hash, resolution_score, file_size, popularity, rank,
  timestamp_verified: true | false,  // Did the extracted clock match the API time?
  extracted_minute: int | null       // The game clock minute extracted by AI (null if no clock)
}
```

These fields enable:
- **Scoped dedup**: New videos only compared against same-status videos in MongoDB
- **Analytics**: Track what % of videos have visible clocks, clock extraction accuracy

### Pipeline data flow
- `validate_video_is_soccer()` extracts structured clock data from 25% and 75% frames
  - Each frame produces: `{clock: str, added: str, stoppage_clock: str}` (raw AI output)
- Per frame: `parse_clock_field()` → clock minutes, `parse_stoppage_clock_field()` → sub-timer minutes
- `compute_absolute_minute()` combines them (frozen-clock case: base + sub-timer)
- `validate_timestamp()` checks if EITHER frame's absolute minute matches API time (only one needs to match!)
- ADDED field cross-checked against `api_extra` (informational log, doesn't affect verification)
- Returns `clock_verified: bool`, `extracted_minute: int|None`, `timestamp_status: str` ("verified", "unverified", or "rejected")
- DownloadWorkflow attaches these to `video_info` dict before passing to UploadWorkflow
- UploadWorkflow includes them in the video object saved to MongoDB

**No MongoDB schema migration needed** — existing videos without `timestamp_verified` are treated as unverified. New videos get the field assigned at validation time.

### The `video_info` dict through the pipeline

```
DownloadWorkflow
  ├── download_video()       → {file_path, file_hash, file_size, duration, ...}
  ├── validate_video()       → adds {clock_verified, extracted_minute, timestamp_verified}
  ├── generate_hash()        → adds {perceptual_hash}
  └── signal UploadWorkflow  → full video_info with all fields

UploadWorkflow
  ├── deduplicate_by_md5()   → filters exact dupes
  ├── deduplicate_videos()   → scoped perceptual hash dedup (verified vs verified, unverified vs unverified)
  ├── upload_single_video()  → S3 upload with metadata
  └── save_video_objects()   → MongoDB with timestamp_verified + extracted_minute
```

MD5 dedup stays unscoped because identical files are identical regardless of what minute they show.

### Fetch event data changes

`fetch_event_data()` in upload.py already loads `existing_s3_videos` from MongoDB with `perceptual_hash`. It needs to also load `timestamp_verified` and `extracted_minute` so that `deduplicate_videos()` can scope S3 comparisons.

This is already done implicitly — `fetch_event_data()` loads the full video object from `_s3_videos`, so any new fields we store will automatically be available. No code change needed here.

---

## Current Models & Data Flow (Comprehensive Reference)

### Model Definitions (`src/data/models.py`)

#### `APIEventTime` (TypedDict)
Source of truth for when an event happened. Comes from the API's `event.time` object.
```python
class APIEventTime(TypedDict, total=False):
    elapsed: Optional[int]   # Base minute (e.g. 45)
    extra: Optional[int]     # Added time (e.g. 2 for 45+2)
```
**No changes needed** — this is the API input we compare against.

#### `EventFields` (Constants class)
String constants for all underscore-prefixed enhanced fields on events. Prevents typos.
```python
class EventFields:
    EVENT_ID = "_event_id"
    MONITOR_WORKFLOWS = "_monitor_workflows"
    MONITOR_COMPLETE = "_monitor_complete"
    FIRST_SEEN = "_first_seen"
    DOWNLOAD_WORKFLOWS = "_download_workflows"
    DOWNLOAD_COMPLETE = "_download_complete"
    DOWNLOAD_COMPLETED_AT = "_download_completed_at"
    TWITTER_SEARCH = "_twitter_search"
    TWITTER_ALIASES = "_twitter_aliases"
    DROP_WORKFLOWS = "_drop_workflows"
    DISCOVERED_VIDEOS = "_discovered_videos"
    S3_VIDEOS = "_s3_videos"
    VIDEO_COUNT = "_video_count"
    DOWNLOAD_STATS = "_download_stats"
    SCORE_AFTER = "_score_after"
    SCORING_TEAM = "_scoring_team"
    REMOVED = "_removed"
    # DEPRECATED
    MONITOR_COUNT = "_monitor_count"
    TWITTER_COUNT = "_twitter_count"
```
**No changes needed** — event-level fields don't change. Videos within `_s3_videos` get new fields, but those are defined in `VideoFields` / `S3Video`.

#### `VideoFields` (Constants class)
String constants for fields on video objects within `_s3_videos`. Currently only covers 6 of the 13+ actual fields:
```python
class VideoFields:
    URL = "url"
    PERCEPTUAL_HASH = "perceptual_hash"
    RESOLUTION_SCORE = "resolution_score"
    FILE_SIZE = "file_size"
    POPULARITY = "popularity"
    RANK = "rank"
```
**⚠️ Gap:** The `S3Video` TypedDict has `width`, `height`, `aspect_ratio`, `bitrate`, `duration`, `source_url`, `hash_version`, `_s3_key` — none of these have `VideoFields` constants. This is a pre-existing gap, not caused by this refactor.

**Changes needed:**
```python
class VideoFields:
    # ... existing fields ...
    # NEW: Timestamp verification fields
    TIMESTAMP_VERIFIED = "timestamp_verified"
    EXTRACTED_MINUTE = "extracted_minute"
```

#### `S3Video` (TypedDict)
Full schema for video objects stored in MongoDB's `_s3_videos` array. This is the source of truth — S3 metadata may be truncated.
```python
class S3Video(TypedDict, total=False):
    url: str                  # Relative URL: /video/footy-videos/{key}
    _s3_key: str              # S3 key for direct operations
    perceptual_hash: str      # Hash for deduplication
    resolution_score: float
    file_size: int            # File size in bytes
    popularity: int           # Times this clip was found (default: 1)
    rank: int                 # 1=best, higher=worse
    # Quality metadata
    width: int
    height: int
    aspect_ratio: float
    bitrate: int
    duration: float
    source_url: str           # Original tweet URL
    hash_version: str         # Version of hash algorithm used
```
**Changes needed:**
```python
class S3Video(TypedDict, total=False):
    # ... all existing fields ...
    # NEW: Timestamp verification fields
    timestamp_verified: bool      # True if clock matched API time, False if no clock visible (rejected videos are never stored)
    extracted_minute: Optional[int]  # Game clock minute extracted by AI (None if not visible)
```

#### `DownloadStats` (TypedDict)
Pipeline stage counters stored in `_download_stats` on events. Tracks what happened to each video.
```python
class DownloadStats(TypedDict, total=False):
    discovered: int               # Total discovered from Twitter
    downloaded: int               # Successfully downloaded
    filtered_aspect_duration: int # Filtered by aspect/duration
    download_failed: int          # Failed to download
    md5_deduped: int              # Removed by MD5 dedup
    md5_s3_matched: int           # MD5 matched existing S3
    ai_rejected: int              # Not soccer
    ai_validation_failed: int     # AI timeout/error
    hash_generated: int           # Hash generated ok
    hash_failed: int              # Hash failed
    perceptual_deduped: int       # Perceptual dedup removed
    s3_replaced: int              # Replaced lower quality S3
    s3_popularity_bumped: int     # Existing S3 kept, popularity bumped
    uploaded: int                 # Successfully uploaded
```
**Changes needed:**
```python
class DownloadStats(TypedDict, total=False):
    # ... all existing fields ...
    # NEW: Timestamp rejection tracking
    timestamp_rejected: int       # Clock visible but wrong minute — video discarded
```

#### `DiscoveredVideo` (TypedDict)
Raw video metadata from Twitter scraper. Stored in `_discovered_videos`.
```python
class DiscoveredVideo(TypedDict, total=False):
    video_page_url: str
    video_url: str
    tweet_url: str
    tweet_text: str
    username: str
    views: int
    likes: int
    retweets: int
```
**No changes needed** — these are pre-download, timestamp extraction happens during validation.

#### `EnhancedEvent` (TypedDict)
Full event structure with all tracking fields. Contains `_s3_videos: List[S3Video]`.
**No direct changes needed** — inherits S3Video changes automatically.

### Workflow Input Types

#### `TwitterWorkflowInput` (dataclass) — `src/workflows/twitter_workflow.py:65`
```python
@dataclass
class TwitterWorkflowInput:
    fixture_id: int
    event_id: str
    team_id: int                    # API-Football team ID
    team_name: str                  # "Liverpool"
    player_name: Optional[str]      # Can be None
```
**Changes needed:**
```python
@dataclass
class TwitterWorkflowInput:
    fixture_id: int
    event_id: str
    team_id: int
    team_name: str
    player_name: Optional[str]
    # NEW: Event minute for timestamp verification
    event_minute: int = 0                    # API elapsed minute
    event_extra: Optional[int] = None        # API extra time (45+2 → extra=2)
```
Fields are appended with defaults so existing Temporal workflow histories remain compatible.

#### `DownloadWorkflow.run()` — `src/workflows/download_workflow.py:71`
Currently takes positional args (not a dataclass):
```python
async def run(self, fixture_id: int, event_id: str, player_name: str,
              team_name: str, discovered_videos: list) -> dict:
```
**Changes needed:**
```python
async def run(self, fixture_id: int, event_id: str, player_name: str,
              team_name: str, discovered_videos: list,
              event_minute: int = 0, event_extra: int = None) -> dict:
```
Appended with defaults for Temporal replay compatibility.

### Activity Signatures

#### `validate_video_is_soccer()` — `src/activities/download.py:434`
```python
async def validate_video_is_soccer(file_path: str, event_id: str) -> Dict[str, Any]:
```
**Changes needed:**
```python
async def validate_video_is_soccer(
    file_path: str, event_id: str,
    event_minute: int = 0, event_extra: int = None
) -> Dict[str, Any]:
```
Return value currently:
```python
{
    "is_valid": bool,
    "confidence": float,
    "reason": str,
    "is_soccer": bool,
    "is_screen_recording": bool,
    "detected_features": list,
    "checks_performed": int,
}
```
**New return value:**
```python
{
    "is_valid": bool,              # soccer AND not screen AND timestamp not rejected
    "confidence": float,
    "reason": str,                 # includes timestamp rejection reason if applicable
    "is_soccer": bool,
    "is_screen_recording": bool,
    "detected_features": list,
    "checks_performed": int,
    # NEW: Structured clock extraction
    "clock_verified": bool,            # True if extracted clock matches API time ±1
    "extracted_minute": int | None,     # Best extracted clock minute (None if no clock)
    "timestamp_status": str,           # "verified" / "unverified" / "rejected"
    "extracted_clocks": list[dict],    # Raw structured data per frame:
                                       #   [{clock, added, stoppage_clock}, ...]
}
```
Note: `is_valid` now incorporates timestamp rejection. `timestamp_status` is still returned separately for logging/analytics but DownloadWorkflow only needs to check `is_valid`.

#### `upload_single_video()` — `src/activities/upload.py:700`
```python
async def upload_single_video(
    file_path, fixture_id, event_id, player_name, team_name,
    video_index, file_hash, perceptual_hash, duration, popularity,
    assister_name, opponent_team, source_url,
    width, height, bitrate, file_size, existing_s3_key
) -> Dict[str, Any]:
```
Returns a `video_object` dict that goes into MongoDB:
```python
"video_object": {
    "url", "_s3_key", "perceptual_hash", "resolution_score",
    "file_size", "popularity", "rank",
    "width", "height", "aspect_ratio", "bitrate",
    "duration", "source_url", "hash_version",
}
```
**Changes needed:** Add `timestamp_verified` and `extracted_minute` params, include in `video_object`:
```python
async def upload_single_video(
    ...,
    existing_s3_key: str = "",
    # NEW
    timestamp_verified: bool = False,
    extracted_minute: int = None,
) -> Dict[str, Any]:

# In video_object:
"video_object": {
    # ... all existing fields ...
    "timestamp_verified": timestamp_verified,
    "extracted_minute": extracted_minute,
}
```

#### `deduplicate_videos()` — `src/activities/upload.py:425`
```python
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None,
    event_id: str = "",
) -> Dict[str, Any]:
```
**Signature stays the same** — verification info is already on each `downloaded_files` item (added by DownloadWorkflow after validation). The function's internal logic changes to:
1. Separate inputs by `timestamp_verified` (true or false); rejected videos were already discarded earlier
2. Run Phase 1 (batch clustering) independently within verified and within unverified
3. Run Phase 2 (S3 comparison) scoped — verified winners vs verified S3 videos, unverified winners vs unverified S3 videos
4. Legacy S3 videos (no `timestamp_verified` field) → treated as unverified

### Complete Data Flow (End-to-End)

```
API event.time → {elapsed: 45, extra: 2}
        │
        ▼
MonitorWorkflow (monitor_workflow.py:155)
├── process_fixture_events() returns twitter_triggered list
│   └── Each item has: {event_id, player_name, team_id, team_name, minute, extra, first_seen}
│                                                                    ▲▲▲▲▲▲  ▲▲▲▲▲
│                                                              ALREADY EXISTS in monitor
├── Creates TwitterWorkflowInput(
│       fixture_id, event_id, team_id, team_name, player_name,
│       event_minute=minute, event_extra=extra     ◄── NEW: pass through
│   )
└── Starts TwitterWorkflow
        │
        ▼
TwitterWorkflow (twitter_workflow.py:100)
├── Resolves team aliases
├── Searches Twitter, gets discovered_videos
├── Starts DownloadWorkflow(
│       fixture_id, event_id, player_name, team_name, discovered_videos,
│       event_minute=input.event_minute,            ◄── NEW: pass through
│       event_extra=input.event_extra               ◄── NEW: pass through
│   )
        │
        ▼
DownloadWorkflow (download_workflow.py:71)
├── Step 1: Download videos in parallel
├── Step 2: MD5 batch dedup (verification-agnostic)
├── Step 3: AI Validation
│   └── validate_video_is_soccer(
│           file_path, event_id,
│           event_minute, event_extra               ◄── NEW: pass minute/extra
│       )
│       Returns: {is_valid, clock_verified, extracted_minute, timestamp_status, ...}
│                                                    ▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲
│                                                    NEW: attach to video_info
│   After validation, for each passing video (is_valid=True):
│       video_info["clock_verified"] = validation["clock_verified"]
│       video_info["extracted_minute"] = validation["extracted_minute"]
│       video_info["timestamp_verified"] = (validation["timestamp_status"] == "verified")
│   
│   Rejected videos (timestamp_status == "rejected") have is_valid=False
│       → caught by existing `if not is_valid` check
│       → download_stats["timestamp_rejected"] += 1 (tracked separately for stats)
│
├── Step 4: Generate perceptual hashes (only verified + unverified videos)
├── Step 5: Signal UploadWorkflow with video_info list
│           (each video_info now has timestamp_verified + extracted_minute)
        │
        ▼
UploadWorkflow (upload_workflow.py)
├── Step 3: fetch_event_data()
│   └── Loads existing_s3_videos from MongoDB
│       (automatically includes timestamp_verified + extracted_minute if stored)
├── Step 4: deduplicate_videos(downloaded_files, existing_s3_videos)
│   └── Phase 1: Batch dedup SCOPED BY VERIFICATION (verified vs verified, unverified vs unverified)
│   └── Phase 2: S3 dedup SCOPED BY VERIFICATION
├── Step 6: upload_single_video(
│       ...,
│       timestamp_verified=video_info["timestamp_verified"],     ◄── NEW
│       extracted_minute=video_info.get("extracted_minute"),      ◄── NEW
│   )
│   └── Returns video_object with timestamp_verified + extracted_minute
├── Step 7: save_video_objects() → MongoDB
│   └── video_object now includes timestamp_verified + extracted_minute
```

### The `video_info` dict lifecycle

The `video_info` dict is a plain dict (not a TypedDict) that accumulates fields as it moves through the DownloadWorkflow pipeline. Here's every field at each stage:

**After download_single_video():**
```python
{
    "status": "success",
    "file_path": "/tmp/found-footy/{event_id}_{run_id}/video_0.mp4",
    "file_hash": "abc123...",       # MD5 hash
    "file_size": 2500000,
    "duration": 45.2,
    "width": 1280,
    "height": 720,
    "bitrate": 3500000,
    "source_url": "https://twitter.com/...",
}
```

**After validate_video_is_soccer() — NEW fields attached:**
```python
{
    # ... all download fields ...
    "clock_verified": True,          # ◄── NEW
    "extracted_minute": 46,          # ◄── NEW (None if no clock)
    "timestamp_verified": True,      # ◄── NEW (True if clock matched, False if no clock)
}
```
Rejected videos (clock visible but wrong minute) are DISCARDED at this stage — they never reach dedup or upload.

**After generate_video_hash():**
```python
{
    # ... all above fields ...
    "perceptual_hash": "dense:0.25:a1b2c3...",
}
```

**After batch MD5 dedup (within DownloadWorkflow):**
```python
{
    # ... all above fields ...
    "popularity": 2,                 # Bumped if MD5 duplicates found
}
```

**Sent to UploadWorkflow via signal — all fields above are preserved.**

**After upload_single_video() → video_object for MongoDB:**
```python
{
    "url": "/video/footy-videos/...",
    "_s3_key": "footy-videos/...",
    "perceptual_hash": "dense:0.25:a1b2c3...",
    "resolution_score": 921600,
    "file_size": 2500000,
    "popularity": 2,
    "rank": 0,
    "width": 1280,
    "height": 720,
    "aspect_ratio": 1.78,
    "bitrate": 3500000,
    "duration": 45.2,
    "source_url": "https://twitter.com/...",
    "hash_version": "dense:0.25",
    "timestamp_verified": True,      # ◄── NEW
    "extracted_minute": 46,          # ◄── NEW
}
```

### Call Sites (ALL UPDATED — commit `4dcf3bc`)

| Location | Change Made |
|----------|-------------|
| `monitor_workflow.py` | `TwitterWorkflowInput(... event_minute=minute, event_extra=extra)` |
| `twitter_workflow.py` | DownloadWorkflow args include `input.event_minute, input.event_extra` |
| `download_workflow.py` | `validate_video_is_soccer` args include `event_minute, event_extra`; attaches `clock_verified`, `extracted_minute`, `timestamp_verified` to `video_info`; counts `timestamp_rejected` |
| `upload_workflow.py` | `upload_single_video` args include `timestamp_verified, extracted_minute` |

### Backward Compatibility

1. **Temporal replay safety:** All new params use defaults (`event_minute=0`, `event_extra=None`, `timestamp_verified=False`, `extracted_minute=None`). In-flight workflows replay cleanly — they're treated as unverified (no clock info), matching current behavior.

2. **MongoDB migration:** None needed. Existing `_s3_videos` documents without `timestamp_verified` are treated as unverified. The `total=False` on `S3Video` TypedDict means all fields are optional. `fetch_event_data()` loads full video objects, so new fields are automatically available when present.

3. **DownloadStats:** Existing stats objects without `timestamp_rejected` are valid — `TypedDict(total=False)` makes it optional.

---

## Pre-Implementation Review (2026-02-15)

Code review of the plan against the actual codebase. Updated 2026-02-15 after discussion.

### 1. `parse_response()` returns dict ✅ RESOLVED

`parse_response()` (nested inside `validate_video_is_soccer()`) returns a dict with named keys, matching the pattern used in `scripts/test_structured_extraction.py`:

```python
def parse_response(resp) -> dict:
    """Parse vision model response into structured dict."""
    return {
        "is_soccer": bool,
        "is_screen": bool,
        "raw_clock": str | None,
        "raw_added": str | None,
        "raw_stoppage_clock": str | None,
    }
```

This is a local change — `parse_response()` is a nested function, not exported. The existing soccer/screen tiebreaker logic indexes by key instead of position. All downstream code updated to match.

### 2. Tiebreaker (50%) frame: extract but don't use for validation ✅ RESOLVED

The 50% tiebreaker frame is only extracted when 25% and 75% disagree on soccer/screen detection. Clock data IS extracted from it (because CLOCK/ADDED/STOPPAGE_CLOCK are part of the prompt) but it is **not passed to `validate_timestamp()`**. Only the 25% and 75% frames are used for timestamp validation because:

- The 50% frame doesn't run every time — validation can't depend on it
- Its timestamp is almost certainly between the 25% and 75% values, adding no new signal
- If both 25% and 75% fail timestamp validation, a middle frame won't help

The prompt stays consistent across all frames (no prompt variant without clock questions), keeping the AI querying logic uniform.

### 3. `is_valid` absorbs timestamp rejection ⚠️ DESIGN DECISION

**Current `is_valid`:** `is_soccer AND NOT is_screen`

**Proposed change:** Fold timestamp rejection into `is_valid` at the AI validation layer:

```python
is_valid = is_soccer and not is_screen_recording and (timestamp_status != "rejected")
```

This means:
- `is_valid=True` → video is soccer, not a screen recording, AND clock either matches or isn't visible
- `is_valid=False` → rejected for any reason (not soccer, screen recording, OR wrong game minute)

**Advantage:** DownloadWorkflow stays simple — one `if validation.get("is_valid")` check, no second rejection branch. The `reason` field already explains WHY a video was rejected ("not soccer", "screen recording", "wrong game minute: expected 46, got 72").

**The `timestamp_status` field still exists** for logging and analytics. It's returned alongside `is_valid` so we can track rejection reasons in `download_stats`, but DownloadWorkflow doesn't need to check it separately.

**Impact on DownloadWorkflow:**
```python
# NO CHANGE to the existing check:
if validation.get("is_valid", True):
    # Attach verification fields
    video_info["clock_verified"] = validation.get("clock_verified", False)
    video_info["extracted_minute"] = validation.get("extracted_minute")
    video_info["timestamp_verified"] = validation.get("timestamp_status") == "verified"
    validated_videos.append(video_info)
else:
    rejected_count += 1
    # reason already explains why (not soccer / screen / wrong minute)
    log.info(workflow.logger, MODULE, "video_rejected",
             "Video filtered by AI validation",
             reason=validation.get('reason', 'unknown'),
             timestamp_status=validation.get('timestamp_status'),
             ...)
```

**Tracking timestamp rejections separately:** We still need `timestamp_rejected` in `download_stats` for visibility. `validate_video_is_soccer()` returns `timestamp_status` in the dict, so DownloadWorkflow can count it even though `is_valid` already handles the rejection:

```python
if not validation.get("is_valid", True):
    rejected_count += 1
    if validation.get("timestamp_status") == "rejected":
        timestamp_rejected_count += 1
    ...
```

### 4. No string-based field lookups — use model constants ✅ RESOLVED

Every MongoDB field name must be referenced through model constants (`VideoFields`, `EventFields`, etc.), never as raw strings. The existing `VideoFields` class is incomplete — it only has 6 of the 15+ fields in `S3Video`. This refactor will fix the whole class.

**Current `VideoFields` (incomplete):**
```python
class VideoFields:
    URL = "url"
    PERCEPTUAL_HASH = "perceptual_hash"
    RESOLUTION_SCORE = "resolution_score"
    FILE_SIZE = "file_size"
    POPULARITY = "popularity"
    RANK = "rank"
```

**Updated `VideoFields` (complete):**
```python
class VideoFields:
    """Constants for video object fields in _s3_videos."""
    # Existing fields
    URL = "url"
    S3_KEY = "_s3_key"
    PERCEPTUAL_HASH = "perceptual_hash"
    RESOLUTION_SCORE = "resolution_score"
    FILE_SIZE = "file_size"
    POPULARITY = "popularity"
    RANK = "rank"
    # Quality metadata
    WIDTH = "width"
    HEIGHT = "height"
    ASPECT_RATIO = "aspect_ratio"
    BITRATE = "bitrate"
    DURATION = "duration"
    SOURCE_URL = "source_url"
    HASH_VERSION = "hash_version"
    # NEW: Timestamp verification
    TIMESTAMP_VERIFIED = "timestamp_verified"
    EXTRACTED_MINUTE = "extracted_minute"
```

This applies to all video field access in `upload.py`, `upload_workflow.py`, and `download_workflow.py`. The `S3Video` TypedDict documents the schema; `VideoFields` provides the lookup constants.

**Also applies to `video_info` dict fields** added by DownloadWorkflow (e.g., `video_info[VideoFields.TIMESTAMP_VERIFIED]`). Even though `video_info` is a plain dict (not typed), using constants prevents typos in string keys.

### 5. Validation placement ✅ RESOLVED

Timestamp rejection is handled inside `validate_video_is_soccer()` itself (per item #3 above). The function already determines `is_valid` — timestamp status is just another factor in that determination. No separate check needed in DownloadWorkflow.

### 6. Internal flow through `validate_video_is_soccer()` ✅ RESOLVED

```
validate_video_is_soccer(file_path, event_id, event_minute, event_extra)
│
├── Extract frame at 25%, 75%
├── Call LLM on each → parse_response() returns dict per frame
│   └── Each dict: {is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock}
│
├── Soccer/screen tiebreaker logic (UNCHANGED in structure)
│   ├── If 25% and 75% agree → use that result
│   └── If disagree → extract 50% frame, call LLM, majority vote
│   (50% clock data extracted but NOT used for timestamp validation)
│
├── Determine is_soccer and is_screen_recording — UNCHANGED
│
├── NEW: Collect frame_clocks from 25% and 75% frames ONLY
│   └── [{"clock": raw_clock_25, "added": raw_added_25, "stoppage_clock": raw_stoppage_25},
│        {"clock": raw_clock_75, "added": raw_added_75, "stoppage_clock": raw_stoppage_75}]
│
├── NEW: Call validate_timestamp(frame_clocks, event_minute, event_extra)
│   └── Returns (clock_verified, extracted_minute, timestamp_status)
│
├── NEW: Compute is_valid = is_soccer AND NOT is_screen AND timestamp_status != "rejected"
│
└── Return combined dict with all fields
```

### 7. `fetch_event_data()` must pass through new fields ⚠️ NEW FINDING

**Issue:** `fetch_event_data()` in [upload.py:163-193](src/activities/upload.py#L163-L193) manually picks fields from MongoDB documents into a new `video_info` dict. It does NOT pass through all fields — only explicitly listed ones:

```python
# Current code — only these fields are extracted:
video_info = {
    "s3_url": ..., "_s3_key": ..., "perceptual_hash": ...,
    "width": ..., "height": ..., "bitrate": ..., "file_size": ...,
    "source_url": ..., "duration": ..., "resolution_score": ...,
    "popularity": ...,
}
```

**If we don't add `timestamp_verified` and `extracted_minute` here, S3 dedup scoping (Phase 2) will not work** — existing S3 videos would appear to have no verification status.

**Fix:** Add the new fields to the cherry-pick list:
```python
video_info = {
    # ... existing fields ...
    VideoFields.TIMESTAMP_VERIFIED: video_obj.get(VideoFields.TIMESTAMP_VERIFIED, False),
    VideoFields.EXTRACTED_MINUTE: video_obj.get(VideoFields.EXTRACTED_MINUTE),
}
```

Legacy videos (no `timestamp_verified` field) default to `False` (unverified), which is correct.

### 8. `upload_single_video()` positional args are fragile ⚠️ NEW FINDING

**Issue:** `upload_single_video()` takes **18 positional parameters**, and the call site in [upload_workflow.py:430-449](src/workflows/upload_workflow.py#L430-L449) passes them all as a positional `args=[]` list. Adding two more (`timestamp_verified`, `extracted_minute`) makes this 20 positional params, which is brittle and error-prone.

**This is a pre-existing problem**, not caused by this refactor. But we're making it worse. Consider:

```python
# Current call site (upload_workflow.py:430-449):
args=[
    video_info["file_path"],      # 0
    fixture_id,                    # 1
    event_id,                      # 2
    player_name,                   # 3
    team_name,                     # 4
    idx,                           # 5
    video_info.get("file_hash"),   # 6
    video_info.get("perceptual_hash"),  # 7
    video_info.get("duration"),    # 8
    video_info.get("popularity"),  # 9
    "",                            # 10 assister_name
    "",                            # 11 opponent_team
    video_info.get("source_url"),  # 12
    video_info.get("width"),       # 13
    video_info.get("height"),      # 14
    video_info.get("bitrate"),     # 15
    video_info.get("file_size"),   # 16
    existing_s3_key,               # 17
    # NEW:
    video_info.get("timestamp_verified", False),  # 18
    video_info.get("extracted_minute"),            # 19
]
```

**Resolution for now:** Append with defaults. The signature has `timestamp_verified: bool = False, extracted_minute: int = None` at the end, so Temporal replay safety is preserved. A wider refactor to use dataclasses/kwargs is out of scope but should be tracked.

### Batch test validation (2026-02-15)

The structured extraction prompt and parsing logic were validated on 10 real production goals via `scripts/test_structured_extraction.py`:

| Goal | API Time | Clock Type | Result |
|------|----------|------------|--------|
| Williams (Bilbao) | 90+6 | Running (95:24) | ✅ Verified |
| Moreira (Porto) | 90+4 | Frozen (90:00 + sub 3:11) | ✅ Verified |
| Havertz (Arsenal) | 90+7 | Running (96:46) | ✅ Verified |
| Hofmann (Gladbach) | 90+2 | Frozen (90:00 + sub 1:39) | ✅ Verified |
| Alvarez (Man City) | 45+2 | No clock (celebration) | ⚪ Unverified |
| Griezmann (Atletico) | 14' | Regular (13:52) | ✅ Verified |
| Garcia (Girona) | 6' | Regular (05:27) | ✅ Verified |
| Lookman (Atalanta) | 33' | Regular (32:18) | ✅ Verified |
| Turrientes (Real Sociedad) | 62' | Regular (61:30) | ✅ Verified |
| Baturina (Dinamo Zagreb) | 39' | Regular (38:44) | ✅ Verified |

**9/9 visible clocks verified correctly. 1/1 no-clock correctly marked unverified. 0 false positives. 0 false negatives.**

---

## Implementation Order

### Phase 1: AI Clock Extraction (COMPLETE — deployed 2026-02-16)

| # | Task | Status | Commit / Notes |
|---|------|--------|----------------|
| 1 | Create branch `refactor/valid-deduplication` | ✅ Done | |
| 2 | Add `event_minute` + `event_extra` to workflow inputs | ✅ Done | `TwitterWorkflowInput` dataclass + `DownloadWorkflow.run()` positional args with defaults |
| 3 | Update vision prompt to structured 5-field extraction | ✅ Done | SOCCER / SCREEN / CLOCK / ADDED / STOPPAGE_CLOCK |
| 4 | Update `parse_response()` to return dict | ✅ Done | Returns `{is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock}` |
| 5 | Add structured parsing functions | ✅ Done | `parse_clock_field()`, `parse_added_field()`, `parse_stoppage_clock_field()`, `compute_absolute_minute()` |
| 6 | Update `validate_timestamp()` for structured input | ✅ Done | Accepts `list[dict]` with raw field keys, returns `(bool, int\|None, str)` |
| 7 | Wire `event_minute`/`event_extra` through call sites | ✅ Done | monitor → twitter → download → validate, all with defaults |
| 8 | Add timestamp validation to `validate_video_is_soccer()` | ✅ Done | Calls `validate_timestamp()`, folds rejection into `is_valid` |
| 9 | Attach verification fields to `video_info` in DownloadWorkflow | ✅ Done | `clock_verified`, `extracted_minute`, `timestamp_verified` on each video_info |
| 10 | ~~Add rejected video discard in DownloadWorkflow~~ | ✅ Absorbed | Handled by `is_valid` inside `validate_video_is_soccer()` |

### Phase 2: Verification-Scoped Deduplication (NOT STARTED)

Phase 2 modifies `deduplicate_videos()` to compare videos only within the same verification group. This prevents a verified goal clip from being replaced by an unverified clip of a different match moment.

**Prerequisites:** Phase 1 must be live and producing reliable `timestamp_verified` values. Check today's game data first.

| # | Task | Status | Notes |
|---|------|--------|-------|
| 11 | Scope batch dedup (Phase 1 inside `deduplicate_videos`) | ⬜ TODO | See implementation details below |
| 12 | Scope S3 dedup (Phase 2 inside `deduplicate_videos`) | ⬜ TODO | See implementation details below |
| 13 | Rank verified videos above unverified | ⬜ TODO | See implementation details below |
| 14 | Add integration tests for scoped dedup | ⬜ TODO | Test verified-vs-verified and unverified-vs-unverified clustering |
| 15 | Test with live data | ⬜ TODO | Verify dedup + ranking decisions match expectations |
| 16 | Deploy and monitor | ⬜ TODO | Track verified/unverified distribution, check frontend ranking |

**Note:** Tasks 12-14 from the old plan (upload_single_video params, upload_workflow passthrough, download_stats tracking) were completed as part of Phase 1 since they were needed to get verification data flowing.

#### Task 11: Scope batch dedup — implementation details

**File:** `src/activities/upload.py`, inside `deduplicate_videos()`, Phase 1 section (lines ~479-560)

**Current behavior:** All `successful` videos fed into a single cluster-building loop. One set of clusters, one survivor per cluster.

**New behavior:** Split videos by verification status first, then cluster within each group:
```python
# After filtering successful downloads:
verified = [f for f in successful if f.get("timestamp_verified", False)]
unverified = [f for f in successful if not f.get("timestamp_verified", False)]

# Run clustering independently — verified videos only compared against
# other verified, unverified only against other unverified
verified_clusters = _build_clusters(verified)    # extract to helper
unverified_clusters = _build_clusters(unverified)

# Pick best from each cluster in both groups
cluster_survivors = []
for cluster in verified_clusters:
    survivor = _pick_best_video_from_cluster(cluster) if len(cluster) > 1 else cluster[0]
    cluster_survivors.append(survivor)
for cluster in unverified_clusters:
    survivor = _pick_best_video_from_cluster(cluster) if len(cluster) > 1 else cluster[0]
    cluster_survivors.append(survivor)
```

**Helper to extract:** The cluster-building loop (lines ~487-520) should become a `_build_clusters(videos: list) -> list[list]` helper function since it's now called twice.

**Logging:** Add `verified_count` and `unverified_count` to the `dedup_clustered` log line so we can see the split.

#### Task 12: Scope S3 dedup — implementation details

**File:** `src/activities/upload.py`, inside `deduplicate_videos()`, Phase 2 section (lines ~585-670)

**Current behavior:** Each cluster survivor is compared against ALL `existing_videos_list` entries.

**New behavior:** Each survivor is only compared against S3 videos in the same verification group:
```python
# Pre-split existing S3 videos by verification status
# Legacy videos (no timestamp_verified field) → treated as unverified
existing_verified = [v for v in existing_videos_list if v.get("timestamp_verified", False)]
existing_unverified = [v for v in existing_videos_list if not v.get("timestamp_verified", False)]

for file_info in cluster_survivors:
    is_verified = file_info.get("timestamp_verified", False)
    comparison_pool = existing_verified if is_verified else existing_unverified
    
    # Only compare against same-group S3 videos
    matched_existing = None
    for existing in comparison_pool:
        if _perceptual_hashes_match(file_info["perceptual_hash"], existing.get("perceptual_hash", "")):
            matched_existing = existing
            break
    # ... rest of replace/skip/new logic unchanged
```

**Logging:** Add `verification_group="verified"/"unverified"` to the `s3_replace`, `s3_skip`, and `new_video` log lines.

**Key edge case:** `fetch_event_data()` in upload.py already cherry-picks `timestamp_verified` and `extracted_minute` from MongoDB video objects (added in Phase 1). Legacy S3 videos without these fields default to `False`/`None`, which is correct — they're treated as unverified.

#### Task 13: Rank verified videos above unverified — implementation details

**File:** `src/data/mongo_store.py`, inside `recalculate_video_ranks()` (line ~1200)

**Why this matters:** Timestamp verification isn't just a dedup signal — it's a **quality signal for the frontend**. A verified video (clock matches the event time) is objectively more trustworthy than an unverified one (no visible clock — could be celebration close-up, fan recording, or wrong match). The frontend should always show verified clips first.

**Current ranking sort key:**
```python
videos_sorted = sorted(
    videos,
    key=lambda v: (v.get("popularity", 1), v.get("file_size", 0)),
    reverse=True
)
```
Sorts by `(popularity desc, file_size desc)`. A highly-popular unverified video can outrank a less-popular verified one.

**New ranking sort key:**
```python
videos_sorted = sorted(
    videos,
    key=lambda v: (
        v.get("timestamp_verified", False),  # Verified always first
        v.get("popularity", 1),               # Then by popularity
        v.get("file_size", 0),                # Then by quality
    ),
    reverse=True
)
```

Since Python sorts `True > False`, all verified videos rank above all unverified ones. Within each group, the existing popularity + file_size tiebreaker applies.

**This is a one-line change** — just add `v.get("timestamp_verified", False)` as the first element of the sort tuple.

**Backward compatibility:** Legacy videos without `timestamp_verified` default to `False`, so they sort into the unverified group — same position they'd have had before. No ranking regression for existing data.

### Test Files Created

| File | Purpose | Status |
|------|---------|--------|
| `tests/test_clock_parsing.py` | Unit tests for all parsing + validation functions | ✅ 58 tests pass |
| `scripts/test_structured_extraction.py` | Batch test: structured prompt against 10 real production goals | ✅ 9/9 verified, 1/1 correctly unverified |
| `scripts/test_clock_extraction.py` | Manual test script for end-to-end clock extraction with real videos | ✅ Created |

**Unit test coverage (58 tests, all passing as of `4dcf3bc`):**
- `TestParseBroadcastClock` — 21 tests: all 5 format categories (A-E), edge cases (None/HT/FT), case insensitivity
- `TestValidateTimestamp` — 10 tests: verified/unverified/rejected outcomes, structured frame dicts, OCR correction
- `TestParseClockField` — 12 tests: Categories A/B/C, period indicators, non-parseable values
- `TestParseAddedField` — 5 tests: standard, trailing apostrophe, space, NONE, None
- `TestParseStoppageClockField` — 5 tests: standard MM:SS, single-digit, sub-minute, NONE, None
- `TestComputeAbsoluteMinute` — 5 tests: regular time, running stoppage, frozen stoppage, all-None

**Note:** Tests dir is NOT in the Docker image. Run via mounted volume or inline `docker exec` checks. Sanity-checked in deployed container at `4dcf3bc`.

---

## Files Modified

### Phase 1 (Complete — commit `4dcf3bc`)

| File | Status | What Changed |
|------|--------|---------|
| `src/activities/download.py` | ✅ Done | Structured 5-field prompt, `parse_response()` → dict, 4 per-field parsers, `validate_timestamp()` accepts structured frames, `validate_video_is_soccer()` takes `event_minute`/`event_extra` + folds rejection into `is_valid` |
| `src/data/models.py` | ✅ Done | `VideoFields` expanded (6 → 17 constants), `S3Video` has `timestamp_verified`/`extracted_minute`/`timestamp_status`, `DownloadStats` has `timestamp_rejected` |
| `src/workflows/monitor_workflow.py` | ✅ Done | Passes `event_minute=minute, event_extra=extra` to `TwitterWorkflowInput` |
| `src/workflows/twitter_workflow.py` | ✅ Done | `TwitterWorkflowInput` has `event_minute`/`event_extra` (defaults for replay safety); passed to `DownloadWorkflow.run()` args |
| `src/workflows/download_workflow.py` | ✅ Done | `run()` accepts `event_minute`/`event_extra`; passes to validate; attaches `clock_verified`/`extracted_minute`/`timestamp_verified` to `video_info`; counts `timestamp_rejected` |
| `src/activities/upload.py` | ✅ Done | `fetch_event_data()` cherry-picks `timestamp_verified`/`extracted_minute`/`timestamp_status`; `upload_single_video()` has 2 new params; included in `video_object` |
| `src/workflows/upload_workflow.py` | ✅ Done | Passes `timestamp_verified`/`extracted_minute` from `video_info` to `upload_single_video()` args |
| `tests/test_clock_parsing.py` | ✅ Done | Updated all tests for structured dicts + 4 new test classes (58 total) |

### Phase 2 (Not Started)

| File | Status | What Needs to Change |
|------|--------|---------|
| `src/activities/upload.py` | ⬜ TODO | Scope batch dedup + S3 dedup by `timestamp_verified` inside `deduplicate_videos()` — see Task 11 & 12 details above |
| `src/data/mongo_store.py` | ⬜ TODO | Add `timestamp_verified` as primary sort key in `recalculate_video_ranks()` — see Task 13 details above |

### Files Created During This Refactor

| File | Purpose |
|------|---------|
| `tests/test_clock_parsing.py` | Unit tests for all clock parsing + validation functions (58 tests) |
| `scripts/test_structured_extraction.py` | Batch test for structured prompt on 10 real production goals |
| `scripts/test_clock_extraction.py` | Manual test script for real video testing |

