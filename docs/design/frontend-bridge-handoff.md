# Frontend bridge handoff — consuming the found-footy live feed

**Audience:** the vedanta-systems frontend/BFF agent. **You are the other half of
the live feed.** found-footy (the producer) now publishes three NATS subjects and
serves a small REST API; your job is to bridge NATS → SSE to browsers and render
the fixtures/events/clips. This doc is self-contained — you don't need found-footy
internals, just this contract.

Producer status (2026-08-14): all three subjects emit; the old transition-subjects
are gone; the read API is consolidated. Nothing here is speculative — it's shipped
on `rebuild/go`.

## The one mental model

**REST is truth; NATS is a hint.** Every NATS message is a tiny "something
changed" nudge. The actual data always comes from a REST refetch. This means:

- You can lose NATS messages and self-heal — just refetch.
- **On every (re)connect you re-snapshot the world via REST, you do NOT replay
  missed messages.** (Core NATS is fire-and-forget in dev; a dropped message is
  gone, and that's fine.)
- Renders are idempotent: applying the same refetch twice is harmless.

## The contract

**Authoritative schemas + golden examples:** [`~/workspace/nats/schemas/`](../../../nats/schemas/)
(`envelope.schema.json` + the three `found-footy.*.schema.json` + `examples/`).
Validate against those; the tables below are the summary.

**Every message is wrapped in the standard envelope:**

```json
{ "id": "<uuid>", "ts": "<RFC3339 UTC>", "source": "found-footy-dev",
  "version": 1, "subject": "found-footy.fixture.clock", "payload": { ... } }
```

`source` is `found-footy-dev` or `found-footy-prod` — the environment stamp.
Routing lives in `subject` + `payload`, never in the envelope shape.

**The three subjects** (subscribe to `found-footy.>` or the three individually):

| Subject | Payload | What you do |
|---|---|---|
| `found-footy.fixture.clock` | `{ "fixtures": [ { "fixture_id": 1530158, "minute": 62, "extra": null } ] }` | **Tick in place. NO fetch.** Update each fixture's displayed minute/stoppage directly from the payload. |
| `found-footy.fixture.update` | `{ "fixture_ids": [1530158, 1530163] }` | **Bulk-refetch** `GET /api/v1/fixtures?ids=1530158,1530163`, splice each returned fixture in by `id`, re-bucket by `state`. |
| `found-footy.event.video` | `{ "event_id": "<uuid>", "fixture_id": 1530158 }` | **Refetch that event** `GET /api/v1/events?ids=<event_id>`, splice into its fixture (by `fixture_id`) by matching `event.id`. |

Per monitor cycle, `clock` and `update` are **disjoint** — a fixture with any
structural change is in `update` only (its fresh clock rides the full refetch), so
you never get both for the same fixture in one cycle. A frozen clock (half-time,
pre-kickoff, stalled) emits **nothing** on `clock`.

## The REST API — two data endpoints + a redirect

Base path `/api/v1` on the found-footy `api` service (reachable on the `luv-dev`
network / via Caddy — deployment config, not part of this contract).

### `GET /api/v1/fixtures` — the fixture endpoint (window OR subset)

- **No params → the full window:** every current fixture (staging + active +
  recently-completed). **This is both your initial page load and your reconnect
  snapshot.**
- **`?ids=1530158,1530163` → just those fixtures** (a `fixture.update` batch).
- Returns a **flat `[]fixtureDTO`** — same shape for one, many, or all. Key by
  `id`, bucket by `state` (`staging` / `active` / `completed`).

### `GET /api/v1/events?ids=<uuid>,<uuid>` — the event endpoint (bulk)

- Returns a flat `[]eventDTO` for the requested ids (unknown ids omitted). A
  single event is just `?ids=<one>`.
- Use it for `event.video` — refetch the one event and splice it into the fixture
  you already hold (via `eventDTO.fixture_id` + `event.id`).

### `GET /api/v1/search?q=<query>` — free-text fixture search

- Case-insensitive substring match across **competition (league) name, either team
  name, event scorer name, and event assist name** — one `q` box hits all four
  (`?q=la liga`, `?q=barcelona`, `?q=lewandowski`, `?q=yamal`).
- Returns the **same flat `[]fixtureDTO`** as `/fixtures` (fixtures carry their events
  + live clips), so render results with the component you already use.
- Scope: the retained window (staging + active + recently-completed), kickoff-newest
  first, capped at 100. No date-range param — deeper history isn't retained.
- Empty/whitespace `q` → **400**; no matches → **200 + `[]`**.

### `GET /api/v1/videos/{share_id}` — clip playback

- **302 redirect** to a presigned Garage URL. This is a *playback* endpoint, not a
  data endpoint — point an `<video>`/anchor at it and let the browser follow the
  redirect. `videoDTO.url` is already this path.

## The response shapes

`fixtureDTO ⊃ []eventDTO ⊃ []videoDTO`. Pointers are emitted **explicitly** (no
omitempty) so `null` is meaningful — e.g. `score: null` (not started) ≠
`score: 0` (0-0).

```jsonc
// fixtureDTO
{
  "id": 1530158,
  "state": "active",                       // staging | active | completed — your buckets
  "kickoff": "2026-08-14T16:00:00Z",
  "league": { "id": 135, "name": "Serie A", "season": 2026,
              "country": "Italy", "round": "Regular Season - 1" },
  "home": { "id": 505, "name": "Inter", "score": 2, "winner": null },  // score/winner null until reported
  "away": { "id": 489, "name": "Milan", "score": 1, "winner": null },
  "penalty": null,                          // { "home": 4, "away": 3 } only on a shootout, else null
  "status": { "short": "2H", "long": "Second Half", "elapsed": 62, "extra": null }, // live clock
  "last_activity_at": "2026-08-14T16:47:30Z",  // recency sort key = max(activation, completion, latest known-scorer goal/card). NOT poll/clock/status. Sort by this desc; null pre-kickoff; a VAR-overturned goal reverts it.
  "events": [ /* eventDTO... — present on active/completed; empty on staging */ ]
}

// eventDTO
{
  "id": "<uuid>",
  "fixture_id": 1530158,                    // splice key for event-scope refetches
  "type": "goal", "detail": "normal goal",
  "minute": 62, "extra": null,
  "team": { "id": 505, "name": "Inter" },
  "player": { "id": 1234, "name": "Lautaro" },  // null = unknown scorer (never video-searched)
  "assist": { "id": 5678, "name": "Barella" },  // the assister; null when none
  "phase": "searching",                     // detected | searching | complete | removed
  "debounce_count": 3,                      // 0–3; "confirming N/3" while phase=detected
  "videos": [ /* videoDTO... — empty [] until clips surface */ ]
}

// videoDTO
{
  "share_id": "s_abc123",
  "url": "/api/v1/videos/s_abc123",         // the 302 playback endpoint
  "rank": 1,                                // 1 = primary clip
  "verified": true, "extracted_minute": 62, "popularity": 3,
  "width": 1280, "height": 720, "duration_ms": 7000
}
```

## Initial load + reconnect (the same call)

Both are `GET /api/v1/fixtures` (window):

1. **(Re)connect** → fetch the window → render it (this is your source of truth).
2. **Then** resume applying live deltas (clock ticks, update refetches, video
   refetches).
3. Messages dropped during a disconnect gap are **moot** — the window snapshot
   already reflects current truth. Re-snapshot, never replay.

This is why core NATS (lossy) is fine and correct for you: you always have REST to
re-derive from.

## Rules that are easy to miss

1. **VAR removal is by absence.** When an event is overturned, it *disappears* from
   its fixture's `events[]` on the next `fixture.update` refetch. Treat "an event I
   was showing is gone from the list" as **revoke it + its clips** (its
   `/videos/{share_id}` will 410 on playback anyway). There is no "removed" message
   — the fixture.update + absence is the signal. (`phase: "removed"` exists but you
   mostly won't see it; the event just vanishes from the fixture.)
2. **`phase` and `videos` are orthogonal — render them independently.** Clips can
   surface *during* `searching` and persist into `complete`. "Has clips" is NOT a
   phase. Phase order (first-match-wins): `removed` > `complete` > `searching` >
   `detected`.
3. **`player: null` = unknown scorer** — a goal with no attributed player. It won't
   be searched (stays `detected`, no clips). Render it as a goal, not a bug.
4. **`debounce_count`** (0–3) is the "confirming 2/3" progress while `phase` is
   `detected`. Once discovery triggers, phase moves to `searching`.
5. **`event.video` can arrive for a *completed* fixture.** A clip can land minutes
   after the final whistle (discovery outlives the match). Accept a video update
   for a fixture you've already bucketed as `completed`.
6. **Half-time / frozen clock is silent** on `fixture.clock`. The 1H→HT status
   change rides `fixture.update` (refetch shows `status.short: "HT"`, clock frozen).
7. **`null` ≠ `0`.** `score`/`winner`/`elapsed`/`extra`/`penalty` are null until the
   vendor reports them. Don't render null as 0.
8. **Penalty shootouts are live** via `fixture.update` (score change), surfaced as
   `fixture.penalty: { home, away }`. They are NOT events — no goal event, no clip.

## Bridge architecture note (the one real subtlety)

If your BFF is a stateless NATS→SSE pipe and its **BFF↔NATS** connection blips while
browsers stay connected, the BFF silently misses messages (the browser sees no
disconnect, so it never refetches). Close that seam: **on the BFF's own
NATS-reconnect, push a "resync" SSE event that makes clients refetch the window.**
(A browser↔BFF blip is already handled — the browser's SSE reconnect triggers its
own window refetch.) This is the lossy-core-NATS tax; it's small.

## Scope: dev now, prod later

- **Dev / the Coppa Italia test (now):** workspace NATS on `luv-dev`, **no auth**,
  **core NATS** (no JetStream). `source: found-footy-dev`. Subscribe, bridge,
  refetch-on-connect + resync. That's the whole job.
- **Prod (deferred, not your task yet):** JetStream durable consumer + `nsc`
  accounts/creds/exports — found-footy issue #169 + the `nats/` repo. A durable
  JetStream consumer would auto-heal the BFF↔NATS seam (replacing the manual resync
  above), but it's not needed for the dev test.

## Build checklist

- [ ] Connect to the workspace NATS (`luv-dev`), subscribe `found-footy.>`.
- [ ] Bridge NATS → SSE to browsers.
- [ ] `fixture.clock` → tick minute/extra in place, no fetch.
- [ ] `fixture.update` → `GET /fixtures?ids=` bulk, splice by `id`, re-bucket by `state`.
- [ ] `event.video` → `GET /events?ids=<event_id>`, splice into fixture by `fixture_id`.
- [ ] (Re)connect → `GET /fixtures` window snapshot → render → resume deltas.
- [ ] BFF NATS-reconnect → emit resync → clients refetch the window.
- [ ] Event vanished from `events[]` → revoke it + its clips (VAR).
- [ ] Render `phase` and `videos` independently; handle `player: null`.
- [ ] Playback via `videoDTO.url` (the 302); don't parse the presigned URL yourself.

## Deeper references (optional)

- Producer design: [`docs/design/proposals/nats-producer-rebuild.md`](./proposals/nats-producer-rebuild.md).
- The decisions behind this contract: [`docs/decisions.md`](../decisions.md) —
  2026-08-14 "NATS producer rebuild" + "Composer decoupled" entries.
- The `phase` contract: [`docs/decisions.md`](../decisions.md) 2026-08-13 +
  `internal/domain/event/phase.go`.
