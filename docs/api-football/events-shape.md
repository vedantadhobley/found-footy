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

**Verified from captured API data.** For fixture `1520391`
(Atletico Madrid–Barcelona, 2026-02-12), E. Garcia's own-goal event carried
Atletico's team ID: the beneficiary, not the player's team. Normal counting by
`event.team` therefore assigns own goals to the correct side; Go must not swap
the team. The preserved sample and original analysis live in the archived
[event-matching proposal](../../archive/docs/proposals/event-matching.md#api-verification-own-goal-attribution).

## Open questions the doc doesn't resolve

1. **VAR overturn behavior — RESOLVED (empirically, from prior Python
   work, 2026-07-26).** When VAR cancels a goal, the API **removes the
   original `Goal` element from the events array.** *Sometimes* it also
   adds a separate `Var` / `Goal cancelled` element; sometimes it does
   not. Two consequences for us:
   - Our set-diff debounce handles the removal when the aggregate score also
    supports it: the goal disappears and the score drops → absence votes → the
    event is soft-removed (`removed_reason='var'`). If the score still requires
    the omitted goal, FF-014's consistency guard withholds the vote.
   - The occasional added `Var` element is **harmless** because we do
     NOT track `Var`-type events (TrackableEventType whitelists only
     Goal / Card=Red / Missed Penalty). We never launch a search for it.
   - **Explicitly out of scope (maybe someday):** tracking VAR-cancelled
     goals via *both* the removal *and* the added `Var` element, which
     would require matching the two against each other to avoid
     duplicate searches on the same cancelled goal. Explored in Python
     and abandoned — the two-way matching adds substantial complexity
     for marginal benefit, and the removal path alone already catches
     the cancellation.
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

**Correction from earlier "free-text" claim** — the field is NOT
free-text on any observed event family. Live vendor audit
2026-07-09 (WC + top-5 club fixtures) surfaced discrete enum-like
value sets per parent event type:

**On `Card` events** (foul reason):

| Value | Emitted for |
|---|---|
| `Foul` | Most common. Direct foul. |
| `Argument` | Dissent / arguing with official. |
| `Roughing` | Physical play beyond a foul. |
| `Unsportsmanlike conduct` | Simulation, delay of game, etc. |
| `Serious foul` | Observed on straight red cards. |
| _(null)_ | Vendor sometimes omits the field entirely. |

Modelled as `apifootball.APICardComment` enum with 5 constants +
Parse function that preserves unknown values.

**On `Goal` events** (shootout marker):

| Value | Emitted for |
|---|---|
| `Penalty Shootout` | Every shootout goal + missed shootout penalty. |
| _(null)_ | Regular-play goals. |

Modelled as `apifootball.APIGoalComment` enum with one constant +
`apifootball.HasPenaltyShootoutComment(string) bool` predicate for
the case-insensitive substring match `TrackableEventType` uses.

**On `Subst` / `Var` events**: no observed comment values so far
(vendor emits null). If we ever see values here, extend the
same-shape enum.

## Casing policy: all lowercase internal (2026-07-09)

Vendor is internally inconsistent about casing:
- Doc: `"Red card"` (lowercase 'c'); emission: `"Red Card"` (title case)
- Doc: `"Subst"` (title case); emission: `"subst"` (lowercase)
- Doc: `"Goal cancelled"` / `"Penalty confirmed"` (lowercase second word); emission unverified

Rather than dance around vendor's inconsistencies with per-family
"canonical matches vendor doc" or "canonical matches real emission"
rules, we adopted a uniform **all-lowercase internal representation**
per decisions.md 2026-07-09 lowercase-canonical entry.

- All enum constants (status, event type, event detail, card comment,
  goal comment) are lowercase, preserving vendor's whitespace where
  applicable (`"missed penalty"`, not `"missed_penalty"`).
- Parse functions normalize inputs via `strings.ToLower` and preserve
  unknown values as lowercase too — the enum type has a uniform
  casing invariant regardless of vendor emission.
- Log lines show lowercase; incident triage should normalize when
  cross-referencing vendor console.

## Missed Penalty tracking

**Added 2026-07-09**: `DetailMissedPenalty` on `Type=Goal` now
maps to a new domain event type `TypeMissedPenalty` (NOT `TypeGoal`)
when the comment is NOT `Penalty Shootout`. A saved / missed penalty
in open play is highlight-worthy but semantically different from a
goal; the domain distinction lets the UI display it distinctly.

Shootout misses still filter out via `HasPenaltyShootoutComment`,
matching Python's behavior.
