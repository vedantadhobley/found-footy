# Fixture status codes

**Status: STUB — mostly derived from Python's
`archive/src/utils/fixture_status.py` + our decisions.md 2026-07-07
APIStatus bucketing entry. Verify against docs when possible.**

**Source URL**: <https://www.api-football.com/documentation-v3> →
Fixtures section → Status enum.

## Classification

Per `internal/domain/fixture/fixture.go`
`APIStatus.Terminal()` / `APIStatus.Live()`:

| Bucket | Codes | Semantic |
|---|---|---|
| **Terminal** | `FT`, `AET`, `PEN`, `CANC`, `ABD`, `AWD`, `WO` | Match is done, no more updates expected |
| **Live** (poll every 30s) | `1H`, `2H`, `ET`, `BT`, `P`, `LIVE`, `HT`, `SUSP`, `INT`, `PST` | Fixture is "active" per our Monitor — includes actual play + paused-but-still-active states |
| **Pre-match** (poll every 15 min bucket) | `NS`, `TBD` | Not yet started, waiting |

**Key decision**: `PST` (Postponed), `SUSP` (Suspended), `INT`
(Interrupted) count as Live because they may resume the same day.
See decisions.md 2026-07-07 APIStatus bucketing entry for
rationale.

## Individual codes (from Python)

Description column is from `archive/src/utils/fixture_status.py`:

### Terminal (won't play again)

| Code | Description |
|---|---|
| `FT` | Match Finished (regular time) |
| `AET` | Match Finished (after extra time) |
| `PEN` | Match Finished (after penalty shootout) |
| `CANC` | Match Cancelled (will not be played) |
| `ABD` | Match Abandoned (may not be rescheduled) |
| `AWD` | Technical Loss (awarded result) |
| `WO` | WalkOver (forfeit) |

### Live / Active (may progress)

| Code | Description |
|---|---|
| `1H` | First Half in progress |
| `HT` | Halftime (will resume) |
| `2H` | Second Half in progress |
| `ET` | Extra Time in progress |
| `BT` | Break Time (between periods) |
| `P` | Penalty Shootout in progress |
| `SUSP` | Match Suspended (may resume) |
| `INT` | Match Interrupted (should resume) |
| `LIVE` | Generic live status |
| `PST` | Match Postponed (keep monitoring — may resume same day) |

### Pre-match

| Code | Description |
|---|---|
| `NS` | Not Started (pre-match) |
| `TBD` | Time To Be Defined (pre-match) |

## Gaps

- Docs might have additional codes we haven't encountered (e.g. some
  vendors have `DELAYED`, `POSTP`, etc.).
- Behavior of `PST` when the reschedule date is announced —
  does the fixture's `date` field update automatically? Currently
  we don't handle this well (see decisions.md 2026-07-07 fixture
  activation triggers entry for the deferred PST-reschedule design).

## Doc sections to paste when you have time

- Full enum of status codes with descriptions
- Confirmation of the PST / rescheduling behavior
