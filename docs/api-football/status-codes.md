# Fixture status codes

**Status: Seeded from vendor doc v3.9.3, 2026-07-09.**

**Source**: `docs/api-football/vendor/api-football-v3.9.3.{pdf,html}`
→ "Fixtures" section, "Available fixtures status" table
(PDF pages 58-59).

## Full enum (verbatim from page 58-59)

| Short  | Long                          | Type       | Description                                                                                                          |
|--------|-------------------------------|------------|----------------------------------------------------------------------------------------------------------------------|
| `TBD`  | Time To Be Defined            | Scheduled  | Scheduled but date and time are not known                                                                            |
| `NS`   | Not Started                   | Scheduled  | *(no description in doc)*                                                                                            |
| `1H`   | First Half, Kick Off          | In Play    | First half in play                                                                                                   |
| `HT`   | Halftime                      | In Play    | Finished in the regular time *(doc text — reads like a typo for "Halftime")*                                         |
| `2H`   | Second Half, 2nd Half Started | In Play    | Second half in play                                                                                                  |
| `ET`   | Extra Time                    | In Play    | Extra time in play                                                                                                   |
| `BT`   | Break Time                    | In Play    | Break during extra time                                                                                              |
| `P`    | Penalty In Progress           | In Play    | Penalty played after extra time                                                                                      |
| `SUSP` | Match Suspended               | In Play    | Suspended by referee's decision, may be rescheduled another day                                                      |
| `INT`  | Match Interrupted             | In Play    | Interrupted by referee's decision, should resume in a few minutes                                                    |
| `FT`   | Match Finished                | Finished   | Finished in the regular time                                                                                         |
| `AET`  | Match Finished                | Finished   | Finished after extra time without going to the penalty shootout                                                      |
| `PEN`  | Match Finished                | Finished   | Finished after the penalty shootout                                                                                  |
| `PST`  | Match Postponed               | Postponed  | Postponed to another day; **once the new date and time is known the status will change to Not Started**              |
| `CANC` | Match Cancelled               | Cancelled  | Cancelled, match will not be played                                                                                  |
| `ABD`  | Match Abandoned               | Abandoned  | Abandoned for various reasons (Bad Weather, Safety, Floodlights, Playing Staff Or Referees); **can be rescheduled or not, it depends on the competition** |
| `AWD`  | Technical Loss                | Not Played | *(no description in doc)*                                                                                            |
| `WO`   | WalkOver                      | Not Played | Victory by forfeit or absence of competitor                                                                          |
| `LIVE` | In Progress                   | In Play    | Used in very rare cases. Indicates a fixture in progress but the half-time or elapsed time data is not available     |

## Load-bearing doc notes (page 59)

- **TBD** may indicate an incorrect fixture date or time when it's not
  yet final. Fixtures with TBD are **checked and updated daily** by
  the API. The same applies to **PST** and **CANC**.
- **The fixture id is unique and specific** — *"In no case an id will
  change."* PST → NS reschedule reuses the same fixture id.
- Some competitions have only `final result` (no livescore). In
  those cases the status remains `NS` and only updates in the
  minutes/hours after the match — **up to 48 hours**, depending on
  the competition.
- *"Although the data is updated every 15 seconds, depending on the
  competition there may be a delay between reality and the
  availability of data in the API."*

## Our classification vs the API's

Per `internal/domain/fixture/fixture.go`:

| Our bucket        | Includes                                       | API's own "Type" column |
|-------------------|------------------------------------------------|-------------------------|
| **Live**          | `1H`, `2H`, `HT`, `ET`, `BT`, `P`, `SUSP`, `INT`, `LIVE`, `PST` | In Play + Postponed |
| **Terminal**      | `FT`, `AET`, `PEN`, `CANC`, `ABD`, `AWD`, `WO` | Finished + Cancelled + Abandoned + Not Played |
| **Pre-match**     | `NS`, `TBD`                                    | Scheduled               |

Two notable divergences from the doc's own bucketing:

1. **PST → Live.** The doc types PST as its own bucket ("Postponed").
   We treat it as Live because it can flip back to NS the same day
   when a reschedule is announced (see decisions.md 2026-07-07
   APIStatus bucketing entry). The doc confirms: *"once the new date
   and time is known the status will change to Not Started"* —
   validating that active polling can catch the transition.
2. **ABD → Terminal.** The doc says ABD *may* be rescheduled
   depending on the competition. We treat it as Terminal, which is
   safer but leaves reschedules to daily re-seeding rather than
   in-cycle recovery. **Open question**: should ABD get PST-like
   treatment? See decisions.md 2026-07-09 API-Football doc-seeding
   entry for follow-up.

## Reschedule mechanics

Because fixture IDs are immutable, a PST → NS transition surfaces
in the same fixture record with an updated `date` / `timestamp`.
Our current worker doesn't watch for date-field changes on
already-active fixtures — a deferred behavior noted in
decisions.md 2026-07-07 fixture activation triggers entry. The
doc confirms this transition is a real thing worth handling.
