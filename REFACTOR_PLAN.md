# Refactor: Validation & Deduplication Overhaul

**Branch:** `refactor/valid-deduplication`  
**Parent:** `feature/grafana-logging`  
**Started:** 2026-02-13

## Current Status

| Phase | Status | Progress |
|-------|--------|----------|
| Phase 1: AI Clock Extraction | 🟡 In Progress | 6/10 tasks done |
| Phase 2: Verified/Unverified Dedup | ⬜ Not Started | 0/6 tasks done |

**Completed:**
- ✅ Clock parsing functions (`parse_broadcast_clock`, `validate_timestamp`)
- ✅ Vision prompt updated with CLOCK question
- ✅ `parse_response()` extracts raw clock text
- ✅ `validate_video_is_soccer()` returns `extracted_clocks`
- ✅ Unit tests pass (58 test cases)
- ✅ Structured extraction validated on 7 real videos (13+ frames) — see "Structured Extraction Approach" below

**Next up:**
1. Update vision prompt + parser to use structured three-field extraction (CLOCK / ADDED / STOPPAGE_CLOCK)
2. Wire `event_minute`/`event_extra` through workflow chain to enable timestamp validation

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

**New return:** 5-tuple: `(is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock)`

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

### Data flow changes needed

1. **`TwitterWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields
2. **`DownloadWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields  
3. **`validate_video_is_soccer()`** — add `event_minute: int` and `event_extra: Optional[int]` params
4. **`monitor_workflow.py`** — already has `minute` and `extra` in `twitter_triggered`, just needs to pass them through
5. **Vision prompt** — update to structured three-field prompt (CLOCK / ADDED / STOPPAGE_CLOCK)
6. **Parse response** — return dict with named keys (not positional tuple) — see Pre-Implementation Review below
7. **Parse clock fields** — call `parse_clock_field()` + `parse_stoppage_clock_field()` + `compute_absolute_minute()` per frame
8. **Validate timestamp** — call `validate_timestamp()` with structured frame data + API time
9. **Return value** — add `clock_verified: bool`, `extracted_minute: int|None`, `timestamp_status: str`

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

Currently: all downloaded videos in a single event are compared against each other using perceptual hashes.

**New behavior:**
1. Rejected videos were already discarded in the DownloadWorkflow (before reaching dedup)
2. Separate remaining videos into verified (`timestamp_verified=True`) and unverified (`timestamp_verified=False`)
3. Run perceptual hash clustering **only within verified** and **only within unverified** separately
4. Select cluster winners from each group independently
5. All winners proceed to S3 dedup

#### S3 dedup (`deduplicate_videos` Phase 2)

Currently: batch winners are compared against all existing S3 videos for the event using perceptual hashes.

**New behavior:**
1. Each S3 video in MongoDB has a stored `timestamp_verified` field (true, false, or absent for legacy videos)
2. Batch winners are only compared against S3 videos **in the same verification group**
3. A verified winner is only compared to existing verified S3 videos
4. An unverified winner is only compared to existing unverified S3 videos
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
    "is_valid": bool,
    "confidence": float,
    "reason": str,
    "is_soccer": bool,
    "is_screen_recording": bool,
    "detected_features": list,
    "checks_performed": int,
    # NEW: Structured clock extraction
    "clock_verified": bool,            # True if extracted clock matches API time ±1
    "extracted_minute": int | None,     # Best extracted clock minute (None if no clock)
    "timestamp_verified": bool,        # True=verified, False=unverified (rejected videos don't reach here)
    "extracted_clocks": list[dict],    # Raw structured data per frame:
                                       #   [{clock, added, stoppage_clock}, ...]
}
```

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
│   After validation, for each passing video:
│       video_info["clock_verified"] = validation["clock_verified"]
│       video_info["extracted_minute"] = validation["extracted_minute"]
│       video_info["timestamp_verified"] = (validation["timestamp_status"] == "verified")
│   
│   For rejected videos (timestamp_status == "rejected"):
│       DISCARD — don't proceed to hash generation
│       download_stats["timestamp_rejected"] += 1
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

### Call Sites That Need Changes

| Location | Current call | Change needed |
|----------|-------------|---------------|
| `monitor_workflow.py:201` | `TwitterWorkflowInput(fixture_id, event_id, team_id, team_name, player_name)` | Add `event_minute=minute, event_extra=extra` |
| `twitter_workflow.py:485` | `args=[input.fixture_id, input.event_id, input.player_name, team_aliases[0], videos_to_download]` | Append `input.event_minute, input.event_extra` |
| `download_workflow.py:300` | `args=[video_info["file_path"], event_id]` | Append `event_minute, event_extra` |
| `download_workflow.py:309` | Checks `validation.get("is_valid")` only | Also check `timestamp_status == "rejected"` → discard |
| `download_workflow.py:315` | Appends to `validated_videos` | Attach `clock_verified`, `extracted_minute`, `timestamp_verified` to `video_info` |
| `upload_workflow.py:438` | `args=[..., existing_s3_key]` | Append `timestamp_verified, extracted_minute` |

### Backward Compatibility

1. **Temporal replay safety:** All new params use defaults (`event_minute=0`, `event_extra=None`, `timestamp_verified=False`, `extracted_minute=None`). In-flight workflows replay cleanly — they're treated as unverified (no clock info), matching current behavior.

2. **MongoDB migration:** None needed. Existing `_s3_videos` documents without `timestamp_verified` are treated as unverified. The `total=False` on `S3Video` TypedDict means all fields are optional. `fetch_event_data()` loads full video objects, so new fields are automatically available when present.

3. **DownloadStats:** Existing stats objects without `timestamp_rejected` are valid — `TypedDict(total=False)` makes it optional.

---

## Pre-Implementation Review (2026-02-15)

Code review of the plan against the actual codebase surfaced 6 items that need to be addressed during implementation. All have clear resolutions — documented here so nothing is missed.

### 1. `parse_response()` return type: dict, not tuple

**Issue:** The plan originally said `parse_response()` returns a 5-tuple `(is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock)`. But `validate_timestamp()` takes `list[dict]` where each dict has `{clock, added, stoppage_clock}` keys. Meanwhile, the working batch test script (`scripts/test_structured_extraction.py`) already uses `parse_structured_response()` returning a dict.

**Resolution:** `parse_response()` (nested inside `validate_video_is_soccer()`) will return a **dict** instead of a tuple:

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

This is a local change — `parse_response()` is a nested function, not exported. The existing soccer/screen tiebreaker logic just indexes by key instead of position.

### 2. Tiebreaker (50%) frame: extract clock from it too

**Issue:** When 25% and 75% frames disagree on soccer/screen, a 50% frame is extracted as tiebreaker. The plan didn't specify whether to extract clock data from it.

**Resolution:** Yes — always pass all analyzed frames (2 or 3) to `validate_timestamp()`. The algorithm already uses "any frame matches" logic, so a third frame is free additional signal. If the tiebreaker frame happens to show a clock that matches, we benefit; if not, no harm.

Implementation: after the tiebreaker LLM call, append its parsed dict to the same `frame_clocks` list that collects 25% and 75% results.

### 3. `is_valid` stays separate from timestamp rejection

**Issue:** Currently `is_valid = is_soccer AND NOT is_screen`. Does timestamp rejection change the meaning of `is_valid`?

**Resolution:** No. Keep `is_valid` meaning exactly what it means today (soccer content + not screen recording). Timestamp status is a **separate** field. The DownloadWorkflow gets **two independent rejection checks**:

```python
# Existing check — unchanged:
if not validation.get("is_valid", True):
    rejected_count += 1    # not soccer or screen recording
    continue

# NEW check — timestamp rejection:
if validation.get("timestamp_status") == "rejected":
    timestamp_rejected_count += 1    # wrong minute
    continue

# Both passed — attach verification fields and keep
video_info["clock_verified"] = validation.get("clock_verified", False)
video_info["extracted_minute"] = validation.get("extracted_minute")
video_info["timestamp_verified"] = validation.get("timestamp_status") == "verified"
validated_videos.append(video_info)
```

This means a video can be valid soccer (`is_valid=True`) but still get discarded if it shows the wrong game minute (`timestamp_status="rejected"`). These are conceptually different reasons for rejection.

### 4. Complete `validate_video_is_soccer()` return dict

**Issue:** The plan listed fields to add but didn't show the complete return value.

**Resolution:** The full return dict after changes:

```python
{
    # EXISTING (unchanged):
    "is_valid": bool,              # soccer AND not screen recording
    "is_soccer": bool,
    "is_screen_recording": bool,
    "confidence": float,
    "reason": str,
    "checks_performed": int,
    
    # NEW:
    "clock_verified": bool,        # True if extracted clock matched API time
    "extracted_minute": int | None, # Absolute game minute from AI (None if no clock)
    "timestamp_status": str,       # "verified" / "unverified" / "rejected"
    "extracted_clocks": list[dict], # Raw structured data per frame (for logging/debugging)
}
```

The `extracted_clocks` list contains one dict per analyzed frame (2-3 frames), each with `{raw_clock, raw_added, raw_stoppage_clock}` — the raw AI output before any parsing. This is only for structured logging and debugging.

### 5. DownloadWorkflow rejection/attachment — exact placement

**Issue:** The plan described what happens in prose but the exact placement in the validation loop matters.

**Resolution:** The change goes in `download_workflow.py` inside the `for video_info in successful_videos:` loop, right after the existing `validation.get("is_valid")` check (~line 307-315). Here's the exact diff:

```python
# BEFORE (current code):
if validation.get("is_valid", True):
    validated_videos.append(video_info)
else:
    rejected_count += 1
    # ... log + cleanup ...

# AFTER:
if validation.get("is_valid", True):
    # NEW: Check timestamp rejection (video is soccer but wrong minute)
    if validation.get("timestamp_status") == "rejected":
        timestamp_rejected_count += 1
        log.info(workflow.logger, MODULE, "timestamp_rejected",
                 "Video rejected — wrong game minute",
                 extracted_minute=validation.get("extracted_minute"),
                 event_id=event_id)
        try:
            os.remove(video_info["file_path"])
        except:
            pass
    else:
        # Attach verification fields
        video_info["clock_verified"] = validation.get("clock_verified", False)
        video_info["extracted_minute"] = validation.get("extracted_minute")
        video_info["timestamp_verified"] = validation.get("timestamp_status") == "verified"
        validated_videos.append(video_info)
else:
    rejected_count += 1
    # ... existing log + cleanup (unchanged) ...
```

Also add `timestamp_rejected_count = 0` to the stats initialization and `download_stats["timestamp_rejected"] = timestamp_rejected_count` to the stats update.

### 6. Internal flow through `validate_video_is_soccer()`

**Issue:** The plan described the external interface changes but not how the internal logic of the function changes.

**Resolution:** The function's internal flow becomes:

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
│   NOTE: clock data from tiebreaker frame is also collected
│
├── Determine is_valid (is_soccer AND NOT is_screen) — UNCHANGED
│
├── NEW: Collect frame_clocks list from all analyzed frames
│   └── [{"clock": raw_clock_25, "added": raw_added_25, "stoppage_clock": raw_stoppage_25},
│        {"clock": raw_clock_75, "added": raw_added_75, "stoppage_clock": raw_stoppage_75},
│        ... (optional 50% frame if tiebreaker triggered)]
│
├── NEW: Call validate_timestamp(frame_clocks, event_minute, event_extra)
│   └── Returns (clock_verified, extracted_minute, timestamp_status)
│
└── Return combined dict with all existing + new fields
```

Key implementation detail: the `frame_clocks` list is built as frames are analyzed, not in a separate pass. Each `parse_response()` call produces both the soccer/screen bools (for tiebreaker) and the clock fields (for timestamp validation). We just need to collect both.

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

### Phase 1: AI Clock Extraction (IN PROGRESS)

| # | Task | Status | Notes |
|---|------|--------|-------|
| 1 | Create branch `refactor/valid-deduplication` | ✅ Done | |
| 2 | Add `event_minute` + `event_extra` to workflow inputs | ⬜ TODO | `TwitterWorkflowInput`, `DownloadWorkflow.run()` |
| 3 | Update vision prompt to structured three-field extraction | 🟡 Redesigned | Current: single CLOCK. New: CLOCK + ADDED + STOPPAGE_CLOCK. Tested on 7 videos. |
| 4 | Update `parse_response()` to extract 5-tuple | 🟡 Redesigned | Current: 3-tuple. New: `(is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock)` |
| 5 | Add structured parsing functions | 🟡 Redesigned | `parse_clock_field()`, `parse_added_field()`, `parse_stoppage_clock_field()`, `compute_absolute_minute()`. Current `parse_broadcast_clock()` kept as fallback. |
| 6 | Add `validate_timestamp()` function | ✅ Done | Returns "verified"/"unverified"/"rejected". Update input to accept structured frame data. |
| 7 | Wire `event_minute`/`event_extra` through call sites | ⬜ TODO | monitor → twitter → download → validate |
| 8 | Add timestamp validation to `validate_video_is_soccer()` | ⬜ TODO | Call `validate_timestamp()`, add verification fields to return |
| 9 | Attach verification fields to `video_info` in DownloadWorkflow | ⬜ TODO | Needs #7 + #8 first |
| 10 | Add rejected video discard in DownloadWorkflow | ⬜ TODO | Between validation and hash generation |

### Phase 2: Verification-Scoped Deduplication (NOT STARTED)

| # | Task | Status | Notes |
|---|------|--------|-------|
| 11 | Update `deduplicate_videos()` for verification-scoped dedup | ⬜ TODO | Batch + S3 phases |
| 12 | Add `timestamp_verified` + `extracted_minute` to `upload_single_video()` | ⬜ TODO | Include in `video_object` |
| 13 | Pass new fields through `upload_workflow.py` call site | ⬜ TODO | |
| 14 | Add `timestamp_rejected` to `download_stats` | ⬜ TODO | Track rejected video count |
| 15 | Test with live data | ⬜ TODO | |
| 16 | Deploy and monitor | ⬜ TODO | Track verified/unverified distribution |

### Test Files Created

| File | Purpose | Status |
|------|---------|--------|
| `tests/test_clock_parsing.py` | Unit tests for `parse_broadcast_clock()` and `validate_timestamp()` | ✅ 58 tests pass |
| `scripts/test_clock_extraction.py` | Manual test script for end-to-end clock extraction with real videos | ✅ Created |

**Unit test results (2026-02-16):** 58 test cases pass — verified in Docker container. Tests cover all 5 format categories (A-E), edge cases (None/HT/FT), case insensitivity, whitespace, and OCR correction scenarios.

**Note:** When implementing the structured extraction, tests will need updating to cover the new per-field parsers (`parse_clock_field`, `parse_added_field`, `parse_stoppage_clock_field`, `compute_absolute_minute`) and the updated `validate_timestamp` signature accepting structured frame data.

---

## Files to Modify

| File | Status | Changes |
|------|--------|---------|
| `src/activities/download.py` | 🟡 Partial | ✅ Prompt + `parse_response()` + `parse_broadcast_clock()` + `validate_timestamp()` + `extracted_clocks` return. 🟡 Redesign: Switch to structured three-field prompt, add `parse_clock_field()` + `parse_added_field()` + `parse_stoppage_clock_field()` + `compute_absolute_minute()`, update `parse_response()` → 5-tuple, keep `parse_broadcast_clock()` as fallback. ⬜ Still need: signature change for `event_minute`/`event_extra`, call `validate_timestamp()`, return verification fields |
| `src/data/models.py` | ⬜ TODO | Add `TIMESTAMP_VERIFIED` + `EXTRACTED_MINUTE` to `VideoFields`, add fields to `S3Video` TypedDict, add `timestamp_rejected` to `DownloadStats` |
| `src/workflows/monitor_workflow.py` | ⬜ TODO | Pass `event_minute=minute, event_extra=extra` to `TwitterWorkflowInput` |
| `src/workflows/twitter_workflow.py` | ⬜ TODO | Add `event_minute`, `event_extra` to `TwitterWorkflowInput` dataclass; pass to `DownloadWorkflow.run()` args |
| `src/workflows/download_workflow.py` | ⬜ TODO | Accept `event_minute`/`event_extra` in `run()`; pass to `validate_video_is_soccer()`; attach verification fields to `video_info`; discard rejected videos; add `timestamp_rejected` to `download_stats` |
| `src/activities/upload.py` | ⬜ TODO | Verification-scoped dedup in `deduplicate_videos()` (both phases); add `timestamp_verified`/`extracted_minute` params to `upload_single_video()`; include in `video_object` |
| `src/workflows/upload_workflow.py` | ⬜ TODO | Pass `timestamp_verified` + `extracted_minute` from `video_info` to `upload_single_video()` args |

### New Files Created

| File | Purpose |
|------|---------|
| `tests/test_clock_parsing.py` | Unit tests for clock parsing functions |
| `scripts/test_clock_extraction.py` | Manual test script for real video testing |

