# API-Football Adapter Audit — 2026-07-09

Cross-referenced the four seeded `docs/api-football/*.md` reference files
against the Go codebase in one overnight sweep. Findings organized so
morning triage can move fast.

**Method.** Four parallel `Explore` subagents, one per doc topic
(events-shape, fixtures-endpoint, status-codes, rate-limits). Each agent
was told to categorize findings as **MISMATCH** (doc-vs-code
disagreement — likely bug), **GAP** (documented feature we don't use),
or **SAFE_DIVERGENCE** (intentional, documented, or acceptable). This
document is the aggregate + curator's opinion on each.

**Nothing was changed overnight.** All items below are proposals.

---

## Recommended action punch list

Ranked by severity/effort. Details in the per-topic sections below.

### Do sooner — real behavior/observability issues

1. **Fix `TrackableEventType` Type-comparison case sensitivity** —
   Docstring at [`event.go:50-51`](../../../internal/domain/event/event.go)
   claims case-insensitive comparison "at the boundary," but the actual
   switch at lines 68 + 76 is exact-match: `case "Goal":`, `case "Card":`.
   Detail comparison at line 66 IS normalized correctly. Works today
   because vendor sends title-case; would silently drop all trackable
   events if the vendor ever normalized to lowercase (Python's config
   uses `subst` lowercase, so casing drift is not hypothetical).
   Two-line fix: either normalize Type like we normalize Detail, or fix
   the docstring to reflect the actual (case-sensitive) behavior.

2. **Log the 429 body** —
   [`client.go:160-167`](../../../internal/infra/apifootball/client.go)
   detects 429 and logs status + endpoint but drops the body. Per doc,
   429 bodies are structured: `{"errors": {"rateLimit": "Too many
   requests"}, "response": [], "results": 0}`. 5xx handling at
   line 171 already reads a body preview — extend the same to 429.
   Python does this (`archive/src/api/api_client.py:97`). Lost signal
   otherwise: no way to distinguish vendor "over quota" from other 429
   sources.

3. **Inspect `errors` field on HTTP 200 responses** —
   `Status()` parses the field into the envelope struct
   ([`client.go:105-114`](../../../internal/infra/apifootball/client.go))
   but never reads it. Same in the fixtures decoder
   ([`fixtures.go:172-180`](../../../internal/infra/apifootball/fixtures.go)).
   Per doc, non-empty `errors` on 200 = soft warning (bad param,
   approaching quota, vendor-side degradation). Python logs as WARN.
   Silent-ignore in Go means we lose the leading indicator before
   hitting hard 429s.

### Do when convenient — clear-benefit gaps

4. **Expose missing `FixtureListParams` fields** —
   `Live`, `team`, `last`, `next`, `round`, `status`, `venue` (all
   documented). User already flagged `Live` as the small-win #1
   before sleep. `status` in particular is worth exposing for the
   Ingest window fetch (could filter to Live+Pre-match only rather
   than pull everything and post-filter).

5. **PST → NS reschedule handling in prod code** —
   `fixture.Reschedule()` exists in
   [`fixture/state.go`](../../../internal/domain/fixture/state.go) but
   is only called by tests. No production code watches for the
   PST → NS transition the doc confirms is real. Deferred per
   `decisions.md` 2026-07-07 entry — still deferred, but the domain
   scaffolding is silently misleading in the meantime (present but
   unused → looks wired when it isn't).

6. **Monitor `UpdateFromPoll` skips `Kickoff` refresh** —
   [`activities.go:200-206`](../../../internal/activity/monitor/activities.go)
   refreshes APIStatus/elapsed/scores but NOT Kickoff. Ingest DOES
   update Kickoff (`ingest/activities.go:230`). Silent divergence
   between the two write paths. Not blocking today because
   PST-reschedule detection is deferred (see #5) — but the day #5
   lands, #6 becomes load-bearing.

7. **Response envelope validation** —
   [`fixtures.go:172-180`](../../../internal/infra/apifootball/fixtures.go)
   deserializes only `response`. Never checks `results` (sanity),
   `paging` (would matter if we ever paginate), or `errors` (see #3).
   Add a minimal wrapper: assert `results == len(response)` at debug
   level, warn on non-empty `errors`.

8. **HTTP 499 (Time Out) treated as generic non-2xx** —
   Doc lists 499 as a distinct documented code (vendor-side timeout).
   Currently ends up in the generic failure bucket. Worth a distinct
   metric outcome label and retry semantics (retry-immediately vs
   backoff makes sense for timeouts specifically).

### Deferred / needs more input

9. **Own-goal team-ID swap** — the doc + our own frozen notes flag
   an unverified rumor that own goals may be reported under the
   SCORING team's ID. No code currently compensates. Don't touch
   until we capture a real own goal into `examples/` and see what
   the API actually sends.

10. **`fixture.periods` / `fixture.referee` decode + expose** —
    Vendor sends both; we drop `periods` entirely and decode `referee`
    but never consume it. `periods.first/second` (absolute period
    start timestamps) could improve elapsed-time sanity checks.
    Referee has no downstream use today — could delete the field or
    plumb through if we ever tag videos with match officials.

11. **Retry-After header** — vendor may or may not send it (docs
    unclear). We never check. Cheapest possible action: log at DEBUG
    when present, so we learn from prod. Not blocking anything.

12. **Silent-block anomaly detection** — vendor doc says
    "excess traffic may be blocked without notice." No observability
    today would catch a silent block cleanly (we'd notice via the
    daily-quota gauge tanking or the calls_total{outcome=failure}
    counter climbing, but that's inference, not detection). Larger
    scope; deferred until we have a real incident to design against.

---

## By-topic details

### 1. events-shape.md ↔ code

**Auditor**: `Explore` — cross-referenced
`internal/domain/event/event.go`, `internal/activity/monitor/activities.go`,
`internal/infra/apifootball/fixtures.go`, plus Python's
`archive/src/utils/event_config.py`.

- **MISMATCH — Type case-sensitivity claim vs implementation** (see
  punch list #1). Docstring lies; behavior works today but claim is
  wrong. Two-line fix.
- **GAP — own-goal attribution swap** (see punch list #9). Depends
  on unverified vendor behavior; need captured samples first.
- **SAFE_DIVERGENCE — comments filter is defensive.** `strings.Contains
  + strings.ToLower` on `apiComments` for "penalty shootout" — case-
  insensitive substring match, robust to whitespace and casing drift.
- **SAFE_DIVERGENCE — nullable field handling.** All nullable fields
  (Player.ID/Name, Assist.ID/Name, Time.Extra) are typed as pointers
  and dereferenced with nil-checks. No panic paths.
- **SAFE_DIVERGENCE — Second Yellow not assumed.** Code doesn't look
  for a "Second yellow card" detail — correctly relies on the vendor's
  behavior (per doc: two separate `Yellow Card` events + a `Red card`
  event).

### 2. fixtures-endpoint.md ↔ code

**Auditor**: `Explore` — cross-referenced
`internal/infra/apifootball/fixtures.go`, `client.go`,
`internal/activity/ingest/`, `internal/activity/monitor/`, plus
Python's `archive/src/api/api_client.py`.

- **MISMATCH — Timezone field exposed but never set.**
  `FixtureListParams.Timezone`
  ([`fixtures.go:158`](../../../internal/infra/apifootball/fixtures.go))
  serializes to the query if non-empty; both Ingest
  ([`ingest/activities.go:80-82`](../../../internal/activity/ingest/activities.go))
  and Monitor instantiate params with zero Timezone. Doc says
  Date/From/To are interpreted per-timezone; we silently default to
  UTC. Not a bug — UTC is what we want — but the code shape lies
  about caring. Either wire it explicitly (`Timezone: "UTC"`) or
  delete the field.
- **GAP — `fixture.periods` not deserialized** (see punch list #10).
- **GAP — `fixture.referee` decoded but unused** (see punch list #10).
- **GAP — Response envelope validation absent** (see punch list #7).
- **GAP — `Live` param not exposed** (user's #1 quick win).
- **GAP — Single-fixture GetFixture not implemented.** No wrapper
  for `/fixtures?id=N`. Batch works but we can't call the single-ID
  path. Marginal; every current caller uses batch anyway.
- **GAP — Query params missing from `FixtureListParams`** (see
  punch list #4).
- **SAFE_DIVERGENCE — Lineups/statistics/players inline shape not
  deserialized.** Doc notes they come inline on `id`/`live` queries.
  `APIFixture` defines only `Events[]`. Acceptable: we don't need
  them today.
- **SAFE_DIVERGENCE — Companion endpoints not stubbed.** Doc lists
  6 companion endpoints; none implemented. Doc itself says: "All
  of these are covered inline by `/fixtures?ids=`."
- **SAFE_DIVERGENCE — 30s poll vs doc's 1/min recommend.** Intentional
  and doc-justified: "half the doc's minimum recommended cadence,
  giving us headroom under the update frequency."

### 3. status-codes.md ↔ code

**Auditor**: `Explore` — cross-referenced
`internal/domain/fixture/fixture.go`,
`internal/activity/ingest/activities.go`,
`internal/activity/monitor/activities.go`, workflow files, plus
Python's `archive/src/utils/fixture_status.py`.

- **GAP — PST → NS transition detection unimplemented** (see punch
  list #5). `Reschedule()` handler exists in domain but no prod
  caller.
- **GAP — Monitor `UpdateFromPoll` skips Kickoff refresh** (see
  punch list #6). Load-bearing the day PST-reschedule handling
  lands.
- **SAFE_DIVERGENCE — PST classified as Live.** Doc types PST as
  its own "Postponed" bucket; we treat as Live because same-day
  resumes are common. Documented in
  [`fixture.go:75-82`](../../../internal/domain/fixture/fixture.go)
  and `decisions.md` 2026-07-07 entry.
- **SAFE_DIVERGENCE — ABD classified as Terminal.** Doc says ABD
  "may or may not reschedule depending on competition." We treat
  as Terminal. Deferred question in
  [`docs/api-football/status-codes.md:68-71`](../../api-football/status-codes.md)
  itself.
- **SAFE — All 19 status codes covered.** Terminal (7) + Live (10)
  + Pre-match (2) = 19. Test coverage confirmed.
- **SAFE — String comparison hygiene clean.** Switch-based
  constants, no case normalization, no whitespace trimming — safe
  because vendor sends exact-form codes. Unknown codes default to
  Pre-match (safe fallback, not Live, not Terminal).
- **SAFE — `LIVE` status handling.** No special assumption about
  `elapsed` being non-null; typed as `*int` throughout.
- **SAFE — `TBD` handling.** Classified Pre-match like NS.

### 4. rate-limits.md ↔ code

**Auditor**: `Explore` — cross-referenced
`internal/infra/apifootball/client.go`, `instruments.go`,
`internal/config/apifootball.go`, workflow retry policies, plus
Python's `archive/src/api/api_client.py`.

- **MISMATCH — 429 body not inspected** (see punch list #2).
- **MISMATCH — Soft errors on 200 not inspected** (see punch
  list #3).
- **GAP — Retry-After header not parsed or logged** (see punch
  list #11).
- **GAP — HTTP 499 not distinctly handled** (see punch list #8).
- **GAP — No anomaly detection for silent blocks** (see punch
  list #12).
- **SAFE_DIVERGENCE — Rate-limit headers observed on ALL response
  paths.**
  [`client.go:158`](../../../internal/infra/apifootball/client.go)
  calls `observeRateLimitHeaders` BEFORE status-code branching, so
  gauges update on 429/5xx too. Correct per doc intent.
- **SAFE_DIVERGENCE — Retry policies respect the per-minute burst
  window.** Monitor's `RetryPolicy{Initial: 1s, Backoff: 2, Cap: 10s,
  MaxAttempts: 2}` and Ingest's `{Initial: 2s, Backoff: 2, Cap: 30s,
  MaxAttempts: 3}` won't burst past Pro plan's ~300/min cap when
  combined with the 30s poll cycle + 20-ID chunk parallelism.

---

## Aggregate counts

| Category | Count | Notes |
|---|---|---|
| MISMATCH | 3 | Type case (behavior works today), 429 body (observability), 200 errors (observability) |
| GAP | 11 | 3 quick-fixes worth doing (envelope validation, Live param, 499 handling); 8 deferred |
| SAFE_DIVERGENCE | 12 | All documented / intentional / doc-verified |

**Nothing catastrophic.** The Type-case sensitivity mismatch is the
only one that would silently break real behavior — and only if the
vendor changed casing convention. The observability mismatches
(429 body, 200 errors) are lost signal, not broken behavior.

**Overall the adapter is in good shape** relative to the doc. Most
gaps are optionality we haven't needed yet (query params, companion
endpoints, plumbing for fields we don't use downstream).

---

## Follow-up conversations

- **Load-bearing tomorrow morning**: punch list items 1, 2, 3. All
  small, high-signal, no design changes. Wanted to flag before you
  do #1 (Live field) and #2 (soft errors) — they overlap with punch
  list #3 (soft-errors from this doc IS punch-list-#2 from the
  user-agreed list before sleep). Might roll them together.
- **Design conversation later**: PST reschedule detection (punch #5
  + #6). The domain has the primitives, the workflow layer needs to
  wire them. Not urgent unless a real PST case surfaces in prod.
- **Deferred pending evidence**: own-goal attribution (#9), silent-
  block detection (#12).
