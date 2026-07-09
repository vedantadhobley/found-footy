# api-football / api-sports.io — frozen reference

**Why this exists.** The vendor docs at
<https://www.api-football.com/documentation-v3> are behind a Cloudflare
bot challenge. No agent tool (WebFetch, curl+UA, etc.) can bypass the
JS challenge — verified 2026-07-09. Rather than repeatedly rediscover
the API's behavior from Python code + guesswork, we mirror the
relevant doc sections here as human-copied markdown.

**Human update flow.** When the API adds a field, changes a status
code, or introduces a new event type, the human updating this repo
opens the docs in a browser, copies the relevant section, updates
the appropriate file below, and notes the update date at the top of
that file. Agents reading these files treat them as ground truth
until superseded.

**Source of truth precedence** (when files disagree):
1. This directory — most authoritative for API behavior
2. `archive/src/utils/event_config.py` — Python's accumulated
   wisdom, but frozen at whenever Python was last updated
3. `internal/infra/apifootball/*.go` — what our adapter actually
   sends + parses; observed behavior
4. Live API responses captured under `examples/` — ground truth
   for what the API actually returned at capture time

## Files

| File | Covers | Last updated |
|---|---|---|
| [fixtures-endpoint.md](./fixtures-endpoint.md) | `/fixtures` + `/fixtures?ids=` + response envelope | ⚠️ stub, needs seeding |
| [events-shape.md](./events-shape.md) | Per-fixture events array — types + details + comments | ⚠️ stub, needs seeding |
| [status-codes.md](./status-codes.md) | Fixture status short codes (NS, 1H, HT, FT, PST, ...) | ⚠️ stub, needs seeding |
| [rate-limits.md](./rate-limits.md) | Burst limits, quotas, rate-limit response headers, 429 body | ⚠️ stub, needs seeding |
| [examples/](./examples/) | Real captured API responses (JSON) | as-needed |

## Suggested capture priorities

Order matters — the most Monitor-critical sections first:

1. **events-shape.md** — every distinct `type` + `detail` + `comments`
   value the API can return. Currently the biggest unknown after
   tonight's "Red Card" vs "Red card" bug. Card types especially.
2. **fixtures-endpoint.md** — exact shape of `/fixtures?ids=`
   response envelope + query params.
3. **status-codes.md** — the FIFA status short codes list. Our
   fixture domain's Live()/Terminal() methods encode assumptions
   here.
4. **rate-limits.md** — headers we scrape (already partially covered
   in `internal/infra/apifootball/client.go`) + burst quotas.
5. **examples/** — actual JSON captured from live matches. Useful
   for corpus scenarios that want to mirror real API shape rather
   than my best guess.

## When agents can't find a value here

If an agent needs a field/value that's NOT in these files, correct
behavior is:
1. **Say so explicitly** — "the docs entry for X isn't in
   `docs/api-football/`; I'm going off Python code or observation"
2. **Ask the human** to seed the relevant file OR paste the
   specific detail
3. **Do NOT silently guess casing / values** — that's exactly the
   failure mode this directory prevents.
