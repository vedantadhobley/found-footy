# /fixtures events array — types + details

**Status: STUB. Not yet seeded from docs. Do not treat as authoritative.**

**Source URL** (Cloudflare-blocked to agents):
<https://www.api-football.com/documentation-v3> — Fixtures section →
Events response.

## Structure

Per-fixture events array. Each element:

```json
{
  "time": { "elapsed": 30, "extra": null },
  "team": { "id": 40, "name": "Liverpool", "logo": "..." },
  "player": { "id": 111, "name": "M. Salah" },
  "assist": { "id": 222, "name": "..." },
  "type": "Goal",
  "detail": "Normal Goal",
  "comments": null
}
```

## Type values

Based on `archive/src/utils/event_config.py` (Python's frozen
knowledge, LAST UPDATED unknown):

| Type | Description | Case |
|---|---|---|
| `Goal` | Score-changing event | Title case |
| `Card` | Yellow/red/second-yellow | Title case |
| `Var` | VAR decision | Title case |
| `subst` | Substitution | **lowercase** |

## Detail values by Type

### Type=Goal

Per Python `event_config.py:11`:

| Detail | Track? | Notes |
|---|---|---|
| `Normal Goal` | ✓ | Regular open-play goal |
| `Penalty` | ✓ | Penalty kick converted |
| `Own Goal` | ✓ | Own goal (see attribution note) |
| `Missed Penalty` | ✗ | NOT a goal despite Type=Goal |

**Comments filter**: goals where `comments` field contains
`"Penalty Shootout"` are shootout goals, not match goals — skip.

**Own goal attribution quirk (unverified)**: API may report own goals
with the SCORING team's ID rather than the CONCEDING team's ID. Needs
verification.

### Type=Card

Per Python `event_config.py:24`:

| Detail | Track? | Notes |
|---|---|---|
| `Red card` | ✓ | **NOTE: lowercase 'c' per Python config. Verify.** |
| `Yellow Card` | ✗ | Noise; would flood pg |
| `Second Yellow card` OR `Second yellow` | ⚠️ Unknown | Effectively a dismissal; want to track but exact string not documented in Python |

### Type=Var

Per Python `event_config.py:32`: not tracked, no details enumerated.

### Type=subst

Per Python `event_config.py:38`: not tracked.

## Field-level details

### `time`

- `elapsed` (int) — match minute (1-90 typically, can exceed for extra time)
- `extra` (int, nullable) — stoppage minutes added within a period.
  `elapsed=45, extra=2` = "45+2 minute"

### `player` and `assist`

- `id` can be `null` for early API updates before scorer is identified
- Our natural_key uses `"unknown"` when player.id is null; see
  `event.ComposeNaturalKey`

### `comments`

- Free-text vendor notes
- **Used by our filter** to drop Penalty Shootout goals

## Gaps in our knowledge

Fields we're not sure how the API actually populates:

1. Exact casing of `Red card` — Python config says lowercase `c`,
   scenarios were written with title case before correction.
2. Whether `Second yellow card` has a distinct detail string or
   collapses to `Red card`.
3. Whether `Var` events fire when a goal gets overturned (and if so,
   the detail values).
4. Whether the `Substitution 1`/`Substitution 2` details we saw in
   Python examples are numbered per player or per team.
5. The full enumeration of `Card` detail values (Second Yellow /
   Yellow Card variants).

## Doc sections to paste in when you have time

1. Events response schema (the full JSON envelope for `/fixtures`
   events)
2. Enum tables for `type` + `detail`
3. Sample responses showing edge cases (VAR overturn, own goal,
   penalty shootout)
