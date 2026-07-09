# /fixtures events array — types + details

**Status: Seeded from vendor doc v3.9.3, 2026-07-09.**

**Source**: `docs/api-football/vendor/api-football-v3.9.3.{pdf,html}`
→ "Events" section (PDF page 69). Live URL:
<https://www.api-football.com/documentation-v3> (Cloudflare-blocked
to agents).

Two ways to get events:

- Inline in `/fixtures?ids=…&id=…&live=…` — every fixture response
  includes an `events` array. Confirmed by the request-sample
  comment on page 61: *"In this request events, lineups, statistics
  fixture and players fixture are returned in the response."*
- Dedicated `/fixtures/events?fixture={id}` endpoint — same shape,
  supports optional `team`, `player`, `type` filters.

Update frequency: **15 seconds**. Recommended calls: 1/min for
in-progress fixtures, otherwise 1/day.

## Element structure

```json
{
  "time":   { "elapsed": 30, "extra": null },
  "team":   { "id": 40, "name": "Liverpool", "logo": "..." },
  "player": { "id": 111, "name": "M. Salah" },
  "assist": { "id": 222, "name": "..." },
  "type":   "Goal",
  "detail": "Normal Goal",
  "comments": null
}
```

## Enum table (page 69, verbatim)

| Type    | Detail values                              |
|---------|--------------------------------------------|
| `Goal`  | `Normal Goal`, `Own Goal`, `Penalty`, `Missed Penalty` |
| `Card`  | `Yellow Card`, `Red card`                  |
| `Subst` | `Substitution [1, 2, 3...]`                |
| `Var`   | `Goal cancelled`, `Penalty confirmed`      |

> *"VAR events are available from the 2020-2021 season."* — doc note.

**Casing is authoritative** — this table is our source of truth:

- `Red card` — **lowercase 'c'** on "card" (verified against page 69
  formatted table; Python's config matched).
- `Yellow Card` — title case on both words.
- Type strings are title case (`Goal`, `Card`, `Subst`, `Var`).
  Note: Python's `event_config.py` writes `subst` lowercase; the
  doc's TYPE column shows `Subst`. The API's actual over-the-wire
  string might follow either convention — captured samples in
  `examples/` are ground truth if this ever matters. We don't
  currently track substitutions so the discrepancy is inert.

## What our system tracks

Per `internal/domain/event/event.go` `TrackableEventType`:

| Type + Detail                       | Tracked? | Notes                            |
|-------------------------------------|----------|----------------------------------|
| `Goal` / `Normal Goal`              | ✓        | Regular open-play goal           |
| `Goal` / `Penalty`                  | ✓        | Penalty kick converted           |
| `Goal` / `Own Goal`                 | ✓        | See attribution note below       |
| `Goal` / `Missed Penalty`           | ✗        | Type=Goal but not a goal          |
| `Goal` / \* + `comments` ∋ `Penalty Shootout` | ✗ | Shootout goals, not match goals |
| `Card` / `Red card`                 | ✓        | Dismissal event                  |
| `Card` / `Yellow Card`              | ✗        | Noise; too many per match         |
| `Subst` / \*                        | ✗        | Not scored/highlighted           |
| `Var` / \*                          | ✗ (today) | See open question below         |

## Own goal attribution quirk

**Unverified — doc doesn't say either way.** Python's Twitter search
compensated for a rumored behavior where own goals get reported
against the SCORING team's ID rather than the CONCEDING team. Needs
verification against captured live samples before we bake anything
into Go. If confirmed, `event.NaturalKey` would want to swap the
team field on Detail=`Own Goal`.

## Open questions the doc doesn't resolve

1. **VAR overturn behavior.** If a goal is checked and cancelled by
   VAR, does the ORIGINAL `Goal` element get removed from the
   events array, OR does a separate `Var` / `Goal cancelled` element
   appear alongside it, OR both? Our set-diff debounce handles case
   #1 naturally; case #2 would need explicit handling. Capture a
   real overturn into `examples/` when it happens live.
2. **Second Yellow.** The doc lists only `Yellow Card` and
   `Red card` for the `Card` type — no `Second Yellow` or
   `Second yellow card` detail. Presumably a second yellow shows up
   as a second `Yellow Card` event followed by a `Red card` event
   (the API composes it that way). Verify from captured samples.
3. **`Substitution [1, 2, 3...]`**. The bracket notation suggests
   a substitution index — per-player? per-team? per-match? Not
   documented. We don't track subs so this is inert.

## Field-level details

### `time`

- `elapsed` (int) — match minute (1-90+; can exceed for stoppage)
- `extra` (int, nullable) — stoppage minutes within a period.
  `elapsed=45, extra=2` = "45+2 minute"

### `player` and `assist`

- `id` can be `null` when the API hasn't identified the scorer yet
  (early notifications). Our `event.ComposeNaturalKey` substitutes
  `"unknown"` in that case.
- `assist.id` is `null` for unassisted goals (penalties, direct free
  kicks, own goals).

### `comments`

- Free-text vendor annotations.
- **Load-bearing**: shootout goals carry `"Penalty Shootout"` in
  this field; `TrackableEventType` filters them out on
  case-insensitive substring match.
