# Twitter search query — design proposal

**Status:** design-first draft. Awaiting signoff before Discovery-side implementation lands.

**Cross-refs:**

- [`twitter-port.md`](./twitter-port.md) — Twitter service (T phase), the consumer of this query design
- [`discovery.md`](./discovery.md) — Discovery workflow (O3), the producer that builds queries per event
- [`team-aliases.md`](./team-aliases.md) + [`alias-entity-resolution.md`](./alias-entity-resolution.md) — the alias pipeline whose output feeds one of the query slots
- Python reference — `archive/twitter/session.py` (query URL construction), `archive/src/utils/event_enhancement.py` (query string builder), `archive/src/utils/orchestration_config.py` (age + cadence constants), `archive/src/workflows/twitter_workflow.py` (attempt loop)
- Decision to expand event scope — this proposal (2026-07-21): Python handled goals only; Go handles goals + missed penalties + red cards, still skipping penalty-shootout goals

## Purpose

For every trackable match event (goal / missed penalty / red card), Discovery builds one Twitter search query string, hits the Twitter service's `/search` endpoint, and consumes the returned tweets. This document specifies the exact query construction: what slots exist, what tokens go in them, what operators are added, and how the query evolves across the up-to-N retry attempts an event's Discovery workflow performs.

The design also specifies how the query builder plumbs a future sentiment-tracking mode (per the user's note 2026-07-21) so we can enable it later without rewriting the Discovery pipeline.

## What Python does today (reference baseline)

**Query construction** (`event_enhancement.py:204 build_twitter_search`):

```python
return f"{player_search_name} {team_name}".strip()
```

Where:

- `player_search_name` comes from `extract_player_search_names(player_name)[0]` — surname isolation with initial-stripping and hyphen handling (De Bruyne → `De Bruyne`, M. Maignan → `Maignan`, Hudson-Odoi → `Hudson-Odoi`).
- `team_name` comes from `extract_team_search_name(team_name)` — one distinctive word from the team's canonical name (Manchester City → `Manchester`, Sporting CP → `Sporting`, Atlético Madrid → `Atletico`).

Example outputs: `"Salah Liverpool"`, `"De Bruyne Man City"`, `"Maignan Milan"`.

**Actual URL** (`session.py:552`):

```
video_search_query = f"{search_query} filter:videos"
search_url = "https://twitter.com/search?q=" + quote(video_search_query) + "&src=typed_query&f=live"
```

So the browser navigates to something like:

```
https://twitter.com/search?q=Salah%20Liverpool%20filter%3Avideos&src=typed_query&f=live
```

- `filter:videos` — server-side restriction to tweets carrying video
- `&f=live` — sort by Latest (newest first) instead of the default "Top"
- No engagement filters. No boolean operators. No time bounds in the URL.

**Age filtering** — client-side, walking tweets in Latest order and checking each `<time datetime="…">` element. Stops scroll when a tweet is older than `TWITTER_SEARCH_MAX_AGE_MINUTES = 3`.

**Attempt cadence** (`twitter_workflow.py` + `orchestration_config.py`):

| Constant | Value |
|---|---|
| `TWITTER_MAX_ATTEMPTS` | 15 (safety cap) |
| `TWITTER_REQUIRED_DOWNLOADS` | 10 (target — loop exits when this many downloads registered) |
| `TWITTER_MAX_VIDEOS_PER_ATTEMPT` | 5 |
| `TWITTER_ATTEMPT_SPACING_SECONDS` | 60 (~1 min between search attempts) |
| `TWITTER_ATTEMPT_MIN_WAIT_SECONDS` | 10 (floor to prevent spin-loop) |
| `TWITTER_SEARCH_MAX_AGE_MINUTES` | 3 (client-side tweet-age cutoff) |

Loop terminates when `download_count >= 10` OR `attempts >= 15`.

## What's wrong with Python's design

1. **The RAG alias pipeline outputs are never used in the query.** Python computes ~5–10 team aliases per team, stores them on the event, logs them extensively — and the actual search string uses `extract_team_search_name(team_name)` which returns ONE distinctive word from the team's canonical name. All the alias-selection machinery is dead weight for query construction.
2. **Single-word team term misses fan nicknames.** `extract_team_search_name("Manchester City") = "Manchester"`. Tweets saying "MCFC absolute scenes!" or "Sky Blues on top!" match zero words of the query and are lost.
3. **No fallback strategy per attempt.** If the primary query returns 0 hits, the attempt just returns empty and the workflow sleeps a minute to try the same query again. That's fine when new tweets are streaming in (they will be, given a real goal), but it wastes the retry budget when the query is subtly wrong (unusual player name transliteration, ambiguous team word, etc.).
4. **Event-type coverage is goal-only.** Python's `TrackableEventType` filter drops missed penalties in open play and red cards; Go's already-shipped domain classification tracks all three. The query builder can extend without new vocabulary — the video is the discriminator.

## Design decisions for the Go query builder

### D1 — Query shape (MVP)

**Everything into a single OR clause, ANDed with `filter:videos`.** Recall-oriented — we'd rather see a tweet that mentions the team OR the player than miss one that mentions only one of the two.

```
({playerTok1} OR {playerTok2} OR ... OR {alias1} OR {alias2} OR ...) filter:videos
```

**Slot definitions:**

- **Player tokens** — every output of `tokenize(event.player.name)` (see D8). Same tokenizer as team aliases, so same diacritic strip / dash split / ≤2 char drop / skip-list behavior.
- **Team aliases** — every entry in `team_aliases.aliases[]` for the scoring team. No cap on count; no ranking. If our alias pipeline says a team has 14 aliases, all 14 go in.
- **`filter:videos`** — literal, unchanged from Python.

**Combined into a single flat OR list.** Parens wrap the whole OR clause so `filter:videos` is ANDed with the group. Order doesn't matter — Twitter treats OR as commutative.

**URL parameters** (unchanged from Python):

- `q={url-encoded query}` — the query as above
- `src=typed_query` — matches Python; helps X's rate-limiting treat the request as user-typed
- `f=live` — sort by Latest (newest first)

**Length expectation:** with 5–14 team aliases and 1–3 player tokens, queries typically run 50–200 chars. Extreme case (Barcelona with 14 aliases + 3 player tokens): ~250 chars. Well under Twitter's ~500 char query limit. Length assertion warns only if >400 chars — a signal of runaway alias generation, not a hard failure.

### Concrete examples

| Event | Query |
|---|---|
| Salah goal for Liverpool | `(salah OR liverpool OR reds OR lfc OR scousers) filter:videos` |
| De Bruyne assist for Man City | `(kevin OR bruyne OR blues OR citizens OR city OR cityzens OR man OR manchester OR mcfc OR sky) filter:videos` |
| Alexander-Arnold red card | `(alexander OR arnold OR liverpool OR reds OR lfc OR scousers) filter:videos` |
| Yamal goal for Barcelona | `(yamal OR azulgrana OR barca OR barcelona OR blaugrana OR culers OR fcb OR ... ) filter:videos` |
| Nathan Aké OG for Man City | `(nathan OR ake OR blues OR citizens OR city OR mcfc OR ... ) filter:videos` |
| Elneny red card for Egypt (national) | `(mohamed OR elneny OR egypt OR egyptian OR pharaohs OR ...) filter:videos` |

### Why OR everything (recall-first)

- **Bilingual / regional fan communities** — a Spanish fan tweeting `"Culers, quéla grande!"` after a Yamal goal mentions the club nickname but not the player. Under Python's AND-both rule, missed. Under OR-anything, caught.
- **Player-only tweets** — a personal-brand tweet like `"MBAPPÉ. WHAT A GOAL."` might not mention Real Madrid at all. AND-both misses it; OR-anything catches it via the player token.
- **Cross-language coverage** — our alias sets include native-language variants (`munchen`, `catalunya`, `nizza`). Fans in those languages tweet using those forms and won't include the English team name.
- **Cost of noise is low** — `filter:videos` cuts most non-football content server-side. The 3-min age scroll-stop bounds walking time regardless of match count. LLM validation downstream verifies each candidate video matches the expected event. Recall failures downstream are cheap; recall failures at query time are unrecoverable.

### D2 — Team alias selection

**No selection.** All aliases from `team_aliases.aliases[]` for the scoring team enter the OR clause. The alias pipeline (see [`alias-entity-resolution.md`](./alias-entity-resolution.md)) already applies the ≥2-lang threshold + English rescue + venue-city skip + non-Latin filter + skip-list to produce a curated set. Trusting that output at query time avoids re-litigating alias quality here.

Python's `extract_team_search_name` word-picker (one distinctive word from the canonical team name) is **not ported** — it was a workaround for the fact that Python's actual query builder DID also OR aliases; the `_twitter_search` field it produced was vestigial. Go drops both the field and the word-picker.

### D3 — Event-type routing (no vocabulary changes)

Per the 2026-07-21 user note: Go handles `TypeGoal`, `TypeMissedPenalty`, `TypeCard` (red only). Penalty-shootout goals are already dropped by `TrackableEventType`.

The query construction is **identical for all three event types**. `filter:videos + player + team` finds the relevant video-carrying tweets regardless. The distinction of what event triggered the search is preserved on the domain event row, but does NOT appear in the search query text.

Rationale for not adding event-type vocabulary (`goal` / `"missed penalty"` / `"red card"`):

- The player name + team term are already strong discriminators — a tweet showing Maignan getting a red card WILL mention Maignan and Milan.
- Adding a required goal-word EXCLUDES tweets that say `"scores!"`, `"nets one"`, `"denied by the keeper"`, `"sent packing"`, `"seeing red"`, etc. Vocabulary is diverse; enforcing a single term shrinks recall.
- Video content IS the discriminator. `filter:videos` catches the video; the video shows what happened. LLM validation downstream verifies the content matches the expected event type (planned in Video pipeline V phase).

If empirical data later shows systematic false positives (e.g. player-team match but wrong event), we can consider a per-event-type positive term. Not for MVP.

### D4 — Time bounds

- **Client-side age cutoff: 3 minutes** — matches Python's `TWITTER_SEARCH_MAX_AGE_MINUTES = 3`. Preserved via config env var `TWITTER_SEARCH_MAX_AGE_MINUTES` (default 3, unit: minutes) so ops can tune per-match if needed.
- **No server-side time filter** (no `since:` or `until:` in the query). Twitter's server-side time operators are unreliable for freshness; scroll-with-age-check is deterministic.
- **Sort order** — always `&f=live` (Latest). The service reads tweets newest-first, walks until a tweet exceeds 3 min age, stops.

### D4b — Preconditions from upstream

The query builder assumes both of these are satisfied before it runs. Enforced by the Discovery workflow's upstream flow, NOT by the query builder itself.

- **`event.player.name` is non-empty.** Python's debounce state machine won't fire the Twitter workflow until the scoring player is known — the api-football event goes through debounce cycles that refine player-nil detections into player-populated ones before triggering downstream. Go MUST preserve this behavior: Twitter workflow is not spawned until debounce has a confirmed player. If the query builder is ever called with `player.name == ""`, that's an upstream bug and should error, not silently query team-only.
- **Team aliases are usually populated at query time.** Alias resolution runs during IngestWorkflow's daily cycle; by kickoff, every team on the fixture card has a resolved `team_aliases` row (or NoMatch, which stores an empty `aliases[]` array). See "alias resolution timing + fallback" below.

### D4c — Alias resolution timing + fallback

**Primary path** — `IngestWorkflow` resolves aliases at fixture-ingestion time via the ResolveAliasesForTeams activity. By the time a match kicks off, `team_aliases.aliases[]` is populated for both sides.

**Fallback**: if for any reason (Nice-class NoMatch, race with fresh fixture, resolution failure) `team_aliases.aliases[]` is empty at query-build time, the builder uses `team_aliases.canonical_name` (the api-football team.name, tokenized) as the sole team-side entry in the OR chain.

Example fallback for hypothetical Nice-class team where aliases stayed empty:
```
Query becomes: (salah OR nice) filter:videos
                       ↑
              canonical_name tokenized
```

**Optional pre-match retry** (deferred, worth calling out): a MonitorWorkflow could retry alias resolution at match start for any tracked team whose row is still unresolved from Ingest. Cheap safety net for Nice-class cases. NOT MVP; add if we see meaningful match-day recall gaps.

### D4d — Empty query safeguard

If the OR clause has ZERO tokens (both player expansion and team aliases produced nothing), the query builder **must refuse to search** — a bare `filter:videos` query returns every video tweet globally and is a disaster.

Guard: emit a WARN observability event (`alias.query.no_tokens` with fixture_id + event_id + team_id + player_name), return "skip" to the caller. Discovery treats it as an attempt that returned zero results — burns an attempt slot, doesn't call Twitter, waits for the next 1-min tick.

In practice this fires only for pathological cases (unresolved team + empty player name — which shouldn't happen per D4b). The guard exists to catch pipeline bugs, not routine cases.

### D5 — Attempt cadence & retry policy

**Same shape as Python**:

| Constant | Go env var | Default | Meaning |
|---|---|---|---|
| Max attempts | `TWITTER_MAX_ATTEMPTS` | 15 | Safety cap per event |
| Attempt spacing | `TWITTER_ATTEMPT_SPACING_SECONDS` | 60 | ~1 min between attempts |
| Min wait floor | `TWITTER_ATTEMPT_MIN_WAIT_SECONDS` | 10 | Prevent spin when attempt returns fast |
| Required downloads | `TWITTER_REQUIRED_DOWNLOADS` | 10 | Loop exits when this many downloads registered |
| Max videos per attempt | `TWITTER_MAX_VIDEOS_PER_ATTEMPT` | 5 | Cap videos discovered per single search |
| Max tweet age | `TWITTER_SEARCH_MAX_AGE_MINUTES` | 3 | Client-side scroll-stop threshold |

Discovery workflow (Go, per event):

```
while download_count < TWITTER_REQUIRED_DOWNLOADS and attempts < TWITTER_MAX_ATTEMPTS:
    query = build_query(event, team_aliases, alternate_index=0)
    videos = twitter_service.search(query, max_age_minutes=3, exclude_urls=already_seen)
    for video in videos[:TWITTER_MAX_VIDEOS_PER_ATTEMPT]:
        start_download_workflow(video)
        already_seen.add(video.tweet_url)
        download_count += 1
    wait max(TWITTER_ATTEMPT_SPACING_SECONDS, TWITTER_ATTEMPT_MIN_WAIT_SECONDS)
```

The 1-minute cadence + 15-attempt cap + 3-min max-age combine to a total observation window of ~15 min per event, which brackets Twitter's typical goal-video appearance window (30s to 5 min post-event, tail extending to ~10 min for slower re-posts).

**Query is IDENTICAL across all attempts.** The only thing that varies attempt-to-attempt is `exclude_urls` (the accumulating set of tweet URLs Discovery has already handed off to downloads). Same broad OR-query hits Twitter each of the 15 attempts; fresh tweets that appear in the 3-min window as time passes get picked up because Latest-sort surfaces newest-first.

**How `exclude_urls` interacts with scroll:**
- Each attempt sends the query + the growing `exclude_urls` set to the Twitter service
- The service walks tweets in Latest order, skipping any whose URL is in `exclude_urls`
- The service ALSO uses `exclude_urls` for early-stop: if 3 consecutive tweets in a row are all in the exclude set (`consecutive_stop_threshold`, default 3), scroll stops — no point walking through mostly-known tweets. See twitter-port.md T/c.
- The service still walks past new-to-us tweets that are in the window; the consecutive counter resets on any new match.

### D6 — Twitter service `/search` endpoint contract

**Request** (`POST /search`):

```json
{
  "query": "Salah Liverpool filter:videos",
  "max_age_minutes": 3,
  "max_videos": 5,
  "exclude_urls": ["https://x.com/user1/status/12345", ...],
  "sentiment_mode": false
}
```

- `query` — the constructed query string (raw, unquoted; service URL-encodes)
- `max_age_minutes` — client-side scroll-stop threshold; passed through from Discovery config
- `max_videos` — soft cap on returned videos (matches `TWITTER_MAX_VIDEOS_PER_ATTEMPT`)
- `exclude_urls` — tweet URLs Discovery has already seen (either from prior attempts in this event or from earlier events in this fixture). Twitter service uses these both to skip individual known tweets AND to short-circuit scroll (the consecutive-already-seen early-stop from twitter-port.md T/c). Normalized tweet-ID set on the service side per twitter-port.md §3.
- `sentiment_mode` — see D7 below. Default `false` (video-search mode).

**Response** (`200 OK`):

```json
{
  "videos": [
    {
      "tweet_url": "https://x.com/user1/status/12345",
      "cdn_url": "https://video.twimg.com/...",
      "duration_seconds": 32.5,
      "posted_at": "2026-08-15T14:23:15Z"
    }, ...
  ],
  "tweets_seen": [
    // populated only when sentiment_mode=true (see D7); nil/absent otherwise
  ],
  "stop_reason": "consecutive_already_seen" | "age_cutoff" | "max_scrolls" | "empty" | "error",
  "total_scrolls": 3
}
```

Error responses match the typed taxonomy from twitter-port.md T/c: `error_class` field with values `rate_limited`, `auth_expired`, `browser_dead`, `query_invalid`, `timeout`.

### D7 — Sentiment-tracking hook (design-in, disabled by default)

The user has flagged sentiment analysis as a future feature (2026-07-21 note): eventually track ALL tweets seen, not just video-carrying ones, so we can measure engagement / stop scrolling on saturation.

**Design decisions**:

1. **`sentiment_mode` boolean on the `/search` request.** When `false` (default): current behavior — query includes `filter:videos`, response returns only video-carrying tweets. When `true`: query DROPS `filter:videos`, service walks all tweets, populates `tweets_seen` with lightweight records `{tweet_url, author_handle, text, posted_at, has_video}`.
2. **Storage: new pg table `event_tweets_observed`** (not part of this proposal — schema landing under a separate task when sentiment work starts). For now the field is in the response but the caller discards it.
3. **Wire the switch through Discovery config from day one.** Discovery's search activity accepts a `sentiment_mode` param, defaults false, exposes an env override. When we build the sentiment analyzer later, we just flip the env var.
4. **Query difference in sentiment mode**: drop `filter:videos`, keep everything else. `q=Salah Liverpool&src=typed_query&f=live`. Same client-side age cutoff, same scroll-stop conditions.

This costs ~5 lines in the query builder (one conditional) and ~30 lines in the response contract. Cheap to bake in now; expensive to retrofit later.

### D8 — Player-name tokenization

**Reuse the existing `tokenize()` function from `internal/domain/alias/text.go`.** Task #135 (originally scoped as a separate design) is resolved by this decision — player expansion is identical to team-alias tokenization.

Same rules apply:
- NFD normalize + strip combining marks (é → e, ü → u, ñ → n)
- `ß → ss` preprocessing
- Split on whitespace + hyphens (single dash and en/em dashes)
- Strip periods, commas, apostrophes
- Lowercase
- Drop tokens ≤2 chars
- Drop all-digit tokens
- Drop non-Latin script tokens (post-2026-07-21 filter)
- Apply the multilingual skip-list (`de, van, der, von, la, le, les, el, il, das, di, da` already present — inherited from team skip-list, matches the particles you flagged: `Kevin De Bruyne` → `de` dropped, Egyptian `El X` → `el` dropped)

Verified behavior on your named cases:

| API-Football `player.name` | Tokenizer output |
|---|---|
| `M. Salah` | `{salah}` (initial + period stripped) |
| `Kevin De Bruyne` | `{kevin, bruyne}` (`de` dropped ≤2) |
| `K. De Bruyne` | `{bruyne}` (initial + `de` both dropped) |
| `Trent Alexander-Arnold` | `{trent, alexander, arnold}` (dash split) |
| `Nathan Aké` | `{nathan, ake}` (é → e via NFD, 3 chars passes) |
| `Kylian Mbappé` | `{kylian, mbappe}` |
| `Vinícius Júnior` | `{vinicius, junior}` |
| `Mohamed Elneny` | `{mohamed, elneny}` |
| `Ronaldinho` | `{ronaldinho}` (single-name works) |
| `Robin Van Persie` | `{robin, persie}` (`van` in skip-list) |

Known edge cases:
- **Two-char surnames** (e.g. hypothetical `Xu Li` transliteration) → both tokens fail the ≤2 char rule and the player has no query representation. Team aliases still cover the search. Not a real concern for the current tracked roster; document as a known limit for future East Asian league expansion.
- **Single-name players** (Ronaldinho, Pelé, Ronaldo Fenômeno era) → one token, works fine.
- **Suffixes like Jr / II** — `Vinícius Júnior` → `junior` survives, joins the OR clause. Not ideal (matches many players named Junior) but rare enough not to filter.

Task #135 can be marked resolved by this design. Implementation is a one-line call to the existing tokenizer from the query builder.

## Improvements over Python (concrete list)

| Improvement | MVP? | Rationale |
|---|---|---|
| Event-type expansion (goal + missed penalty + red card) | Yes | Domain already handles it; free win |
| Sentiment-mode hook (query + response contract) | Yes (design, not use) | Cheap now, expensive later |
| OR-everything query shape (recall-first) | Yes | Bilingual/regional/player-only tweets caught |
| Use ALL team aliases (no cap, no ranking) | Yes | Trust the alias pipeline's curation |
| Drop `_twitter_search` legacy field + `extract_team_search_name` word-picker | Yes | Both were vestigial in Python |
| Reuse `tokenize()` for player expansion (closes #135) | Yes | Same tokenizer works for players |
| Length assertion + observability warn (>400 chars) | Yes | Runaway alias generation detection |
| Better error taxonomy (typed error_class) | Yes | Already scoped in twitter-port.md T/c |
| Client-side query building metrics (per-slot token diversity) | No — post-MVP | Only needed once we have a real corpus |

## Wiring back into architecture.md

When Discovery's search activity ships (part of the O3 or Video-pipeline commits), update `docs/architecture.md`'s Discovery-workflow section with:

- One paragraph pointing here + the exact env vars
- Cross-ref to the twitter-port.md `/search` endpoint contract

Per the working discipline in `CLAUDE.md`, this MUST land in the same commit that ships the code — not a follow-up.

## Testing plan

**Unit test** — `discovery/query_builder_test.go`:
- Fixed event fixtures (goal, missed penalty, red card) → assert exact query strings
- Player name edge cases (De Bruyne, M. Maignan, Hudson-Odoi, Ronaldo single-name) → assert player-token slot
- Length assertion: no query > 200 chars
- Sentiment-mode flag: `filter:videos` absent from query when enabled

**Probe script** — `scripts/probe_query/main.go` (parallel to `probe_aliases`):
- Take a hand-crafted event list, print the query string for each
- Print length + slot breakdown
- Compare against Python's builder output for the same events (parity check)

**Integration test** — as part of the T/c → Discovery wire-up:
- Discovery-side workflow builds query for a synthetic event, hits Twitter service `/search`, verifies non-error response shape
- Skipped in `-short`; runs against a mock Twitter service that returns canned tweet fixtures

## Implementation notes (not decisions — just things the implementer needs to know)

- **`OR` MUST be uppercase** in Twitter search query syntax. Lowercase `or` is treated as a searchable word (matches tweets containing the literal string "or"), not the OR operator. Query builder emits `OR` uppercase always.
- **Set-based dedupe.** Combine player tokens + team aliases into a single `map[string]struct{}` in the builder. Same-string tokens across the two sources collapse to one entry, and the sort-for-stable-output step naturally deduplicates. Do the dedupe ONCE — not once for player tokens, once for aliases, once for the merged output.
- **URL encoding.** The query string goes into the `q=` parameter of the search URL. Standard `url.QueryEscape` for the Go builder — matches Python's `quote()` from `urllib.parse`.
- **No `-filter:retweets`, no `lang:`, no engagement filters.** Matches Python. Verified against `archive/twitter/session.py:551` — only `filter:videos` is appended. Retweets don't surface in Latest sort in practice; `lang:` filter would break multilingual recall (Spanish/German/Italian goal tweets); engagement filters would break real-time discovery (goal tweets have zero engagement in the first 30 seconds).
- **`&f=live` always** for MVP. If sentiment-mode ever wants engagement-weighted ordering, add a knob then; not now.

## Open questions before signoff

None material — all edge cases and behaviors resolved in the design above. The three previously-flagged cosmetic items (`&f=live`, length warning threshold, `sentiment_mode` naming) are captured under implementation notes and can be tuned freely without design impact.

## Follow-up work

- **Task #135** (player expansion) — resolved by this design (reuse `tokenize()`). Marking complete.
- **Task #137 implementation** — Discovery-side query builder (Go), plus wiring into the search activity.
- **Twitter service `/search` endpoint** — implement per the D6 contract when T/c ships (Twitter port phase).
- **`event_tweets_observed` schema** — lands when sentiment analyzer work starts; not part of this proposal.
