# Design-improvement notes — 2026-07-23

Running list of design opportunities surfaced during the rebuild — either
places where the current implementation is expedient rather than right,
where new patterns emerged mid-build that the earlier code doesn't reflect
yet, or where empirical evidence pointed at a better shape than what
`rebuild-plan.md` originally speced.

**This is NOT a task list.** These are candidates to consider in a
follow-up design-improvements pass (planned to run after the
2026-07-26 audit lands). Some will become tasks; some will be
consciously deferred; some will be rejected on further thought. All
worth capturing before they evaporate.

Each entry has: **what the improvement is**, **why it beats the current
shape**, **what it would cost**, and **when it makes sense to invest**.

---

## Scaler + Twitter fleet

### 1. pg NOTIFY-based scaler, not polling

Current `#160` T/d design started as poll-every-30s on active-workflow
count. That's a code smell: latency floor equal to poll interval, wasted
queries every tick when nothing changed.

**Better:** pg trigger on `event_downstream_workflows` INSERT +
UPDATE(completed_at) → NOTIFY. Scaler holds a `LISTEN scaler_signal`
connection and reacts in milliseconds instead of seconds.
Belt-and-suspenders 60s heartbeat covers listener disconnects.

**Why it wins:** Firefox cold-start is 15-20s, debounce window is 60-90s.
A 30s poll interval spends up to 30s of that headroom on detection
latency — half the budget for zero reason. NATS-scope entry (2026-07-21)
already sanctions pg NOTIFY as the intra-project push mechanism, so this
uses infra we've committed to.

**Cost:** trivial. One trigger function, one LISTEN loop. Half a day.

**When:** land as part of T/d's initial design, not a v2.

---

### 2. Fully dynamic per-fixture Twitter lifecycle, not Python's static min=2

Python's scaler keeps min=2 warm 24/7, max=8, reactive scaling on
active-goal count. Reactive means we lag the load; static means we
burn ~2 GB Firefox for hours between matches.

**Better:** min=2 warm (redundancy + first-goal-of-day zero-cold-start
still wanted); speculative pre-warm on `events` INSERT WHERE
debounce_count=1 → hide Firefox cold-start behind the 60-90s debounce
window; scale-down grace of 3-5 min; never below min=2.

**Why it wins:** off-hours savings (currently 2×1GB × ~50% non-match
hours ≈ 24 GB-hours/day of idle Firefox). Zero cold-start on hot path.
Same predictable minimum as Python for reliability. Better memory
utilization when the box is doing other work.

**Cost:** medium. New trigger, scaler process, container-name derivation
from pg row identity. 1-2 days.

**When:** T/d, once #162 config work is behind us.

---

### 3. Cycling background jobs in the scaler (memory bounds + cookie freshness + health)

Python doesn't have this at all. Firefox instances live until manually
recycled or the container dies. Memory leaks accumulate; a stale
instance can have expired cookies without knowing until it fails a
search hours later.

**Better:** three periodic jobs in the scaler:
- **Age rotation** every 6h — spawn fresh instance, drain oldest, kill.
  Bounds Firefox memory, keeps at least one instance with fresh cookies
  at all times.
- **Auth-refresh canary** every 1h — one instance nav-to-/home + verify
  + backup. Rotate the canary role. Near-real-time cookie-expiry
  detection instead of finding out mid-match.
- **Health-based replacement** — `/health` failing >30s → drain + spawn
  replacement. Same total count.

**Why it wins:** the T/b + T/b.5 cookie backup gate already knows how to
share cookies; adding periodic re-verification is a natural extension.
Prod experience with Python has surfaced auth-expired mid-match at
least twice — cycling would have caught both hours earlier.

**Cost:** small on top of #2. Half a day.

**When:** part of T/d.

---

### 4. Cookie serialize-on-cold-start

If M twitter instances cold-start in parallel (Saturday afternoon burst,
multiple matches kicking off simultaneously), all M read the same cookie
file, all M hit x.com for auth-verify in the same second. Twitter's
anti-bot heuristics could plausibly flag that as an attack pattern.

**Better:** first instance up in a burst does the auth verify + writes
backup; subsequent instances read the backup within a 60s fast-path
window (extends the existing T/b.5 fingerprint gate). No per-instance
verify unless the backup is stale.

**Why it wins:** cuts M concurrent x.com hits down to 1 per burst.
Cookie-file semantics are already right for this — we just add a
"fresh backup within N seconds → skip re-verify" fast path.

**Cost:** small. Extension of existing gate, ~2 hours.

**When:** with #2 dynamic scaling; needed once M > 3-4 instances can
cold-start in parallel.

---

## Discovery + query design

### 5. Post-hoc query-quality learning from `event_search_candidates`

Every candidate we discover — accepted OR rejected — is now persisted
per #158/O3/d. Miami smoke test surfaced clear query-recall pathologies
(Chicago Fire → "fire" matches wildfires, oil tankers, arson) that the
current recall-first design can't fix without V-phase LLM validation.

**Better:** background analytics job (dagster? cron? one-off script?)
that reads `event_search_candidates` weekly and surfaces:
- Teams whose aliases produce disproportionate false-positive rates
- Common tokens across noise candidates (`fire`, `city`, `united`, ...)
- Recall-precision curves per team

Feeds back into the alias resolver's skip-list refinement.

**Why it wins:** turns Discovery's storage into a learning loop
without changing the runtime path. Complements V-phase (which filters
noise) with data to improve query construction (which prevents noise
from being asked for in the first place).

**Cost:** low priority for Aug 14, but the storage substrate is already
there. Half-day for a first-pass analytics query.

**When:** post-cutover. NOT Aug-14-blocking. Include in follow-up plan.

---

### 6. Cross-event candidate dedup at video-asset level, not per-event

Miami smoke test: R. Rios own goal + L. Suarez penalty happened in the
same match. Two Discovery workflows ran with overlapping team aliases
(`chicago`, `fire`, `inter`, `miami`). One tweet (Polish-language own
goal video) landed as a candidate under BOTH events.

Current per-event dedup can't fix this — each event has its own
`seenTweetIDs` map. The right dedup layer is the eventual `video_assets`
table: same underlying video (perceptual hash match) → single asset row,
multi-event association via a link table.

**Better:** V-phase's dedup activity checks perceptual hash against the
existing corpus; on match, LINK the new event to the existing asset
rather than creating a duplicate.

**Why it wins:** every fresh event that discovers a
previously-seen-in-another-event video doesn't produce redundant
storage / API surface entries. Matches the rebuild-plan.md §3 schema
intent (video_assets is the perceptual identity, events reference it).

**Cost:** already in rebuild-plan.md scope for V-phase. This is a note
that Miami evidence CONFIRMS the design is right.

**When:** V-phase, exactly as specced.

---

### 7. Wall-clock filter fallback for degraded Twitter recall

Decisions.md 2026-07-23 wall-clock entry acknowledges: if Twitter's
non-bounded timeline search ever degrades (recent tweets stop
surfacing in ordered order), we have no fallback. Bounded search
proven unreliable.

**Better:** monitor `candidates_found` per event via metrics. If the
median drops below N over a rolling window, alert. Manual fallback
paths (bounded search retry with wider windows, alternate query
constructions, whatever) get investigated when the alert fires.

**Why it wins:** we don't design against a specific failure mode we
haven't seen — but we DO monitor for the failure signal so we know
when it's happening.

**Cost:** metric + Grafana alert. Half a day. Metric probably already
partly exposed via Discovery workflow output.

**When:** post-V-phase (when we have LLM-validated survival rate as
the more useful signal). Not blocking Aug 14.

---

## NATS + observability

### 8. Envelope + subject-scheme migration to match 2026-07-23 dhobley standard

**Known drift** — currently:
```
subject:   fixture.activated / event.detected
envelope:  {event_log_id, kind, occurred_at, event_id, fixture_id, payload}
```

New standard:
```
subject:   <project>.<env>.<service>.<domain>.<action>
           e.g. found-footy.prod.worker.fixture.activated
envelope:  {id, ts, source, version, payload}
```

**Migration path:**
- Publisher side: composer.go transforms envelope + subject to new
  standard. `source` field becomes the container name literally.
  Payload keeps its existing shape (schema-per-subject in
  `~/workspace/nats/schemas/`).
- Lift `internal/infra/nats/` → `~/workspace/nats/clients/go/` as the
  shared library (found-footy is the pilot per cutover plan
  workstream G).

**Why it wins:** unblocks vedanta-systems BFF from subscribing without
custom-mapping our non-standard envelope. Every future NATS producer
inherits the pilot's shape. Compliance with the 2026-07-23 decisions.

**Cost:** medium. 1-2 days for the shared library extraction + envelope
change + subject rename. Cross-project — needs coordination with
vedanta-systems for its subscriber to update in lockstep.

**When:** BEFORE the first real subscriber (vedanta-systems BFF) wires
up. Ideally before we ship `foundfooty.classify.done` in V-phase.

---

### 9. Metrics endpoint conformance check

Decisions.md 2026-07-23 custom-app-metrics entry: `/metrics` on primary
HTTP port, `foundfooty_` prefix, `service`/`env`/`node` labels.

I don't know yet whether our current metrics fully conform — this is a
compliance check the audit will surface. If not conformant:

- Add /metrics handler on cmd/worker, cmd/api, cmd/twitter, cmd/scaler
- Prefix all metric names with `foundfooty_` if not already
- Add `service`/`env`/`node` labels via a shared metrics helper (matches
  the "labels added by the shared helper, not per-project" convention)

**Why it wins:** required for Netdata + Grafana to scrape correctly.
Compliance = free integration with the observability substrate.

**Cost:** depends on gap. Best case a few hours (add labels). Worst case
a day (add handler + refactor prefixes across every metric).

**When:** before Aug 14 for sure. Waiting on audit findings to size.

---

## Testing + development

### 10. Test-quality audit — mocks that prove nothing

Multiple test files in the workflow layer follow the pattern:
- Mock every activity with `.Return(SuccessOutput{}, nil)`
- Execute workflow
- Assert `workflow.IsCompleted() && err == nil`

That doesn't test workflow logic — it tests "the workflow function's
control flow doesn't blow up given inputs it happens to accept." Real
bugs (wrong ordering, wrong argument construction, wrong branch
selection) survive this pattern.

**Better:** the workflow tests that matter are the ones that assert:
- **Which activities got called** (via `AssertExpectations`)
- **What arguments** (via `mock.MatchedBy(...)` predicates)
- **What order** (via sequence assertions where order matters)
- **What branch** was taken (empty-query → early return, etc.)

Our discovery_test.go already does this well. Others may not. The
audit's test-coverage probe will identify which ones need upgrading.

**Cost:** per-file, low. Cumulative across the codebase, a solid day
of test-tightening work.

**When:** deferrable — doesn't block Aug 14 delivery, but does inform
how much we trust the test suite. Do after audit surfaces the specific
files.

---

### 11. Ledger doc discipline enforcement

CLAUDE.md working discipline (2026-07-07 retro closure) requires
ledger docs (architecture.md, orchestration.md, observability.md,
temporal.md, testing.md, deployment.md) to be updated in the same
commit as the code that changes them. I already found one row today —
orchestration.md had DiscoveryWorkflow listed as "⊘ O3 planned" long
after O3/d shipped.

**Better options:**
- **A. Pre-commit hook** — grep changed files against ledger doc list;
  if any workflow / adapter / config code changed without a
  corresponding ledger doc file also being in the changeset, warn.
  Not enforce — code + docs sometimes legitimately split.
- **B. CI check** — same idea, in CI, produces a comment rather than
  blocks merge.
- **C. Per-session close-out ritual** — before committing a phase,
  read the relevant ledger, grep for staleness. Documented in
  CLAUDE.md.

**Why it wins:** the retro that produced the discipline exists BECAUSE
this drift was expensive to fix once. A gentle nudge at commit time
prevents recurrence without the process theater of a hard gate.

**Cost:** A is ~1 hour of shell script. B is ~half a day of GH Actions
work. C is free.

**When:** post-audit — the audit will surface how many ledger rows
are stale. If it's several, invest in A or B. If it's just today's
one, C is enough.

---

## Infrastructure conventions

### 12. Container labels + env vars for structured identity access

Per today's dhobley infra conversation: every service should export
identity segments via both Docker labels (`com.vedanta.project`,
`com.vedanta.env`, `com.vedanta.service`) and env vars
(`NATS_PROJECT`, `NATS_ENV`, `NATS_SERVICE`). Container name stays
DNS-safe dashed; NATS subject prefix is derived from the same triple.

**Why it wins:** three surfaces (container name / labels / env vars)
express the same identity, no parsing required. Loki `{project=...}`,
NATS subject prefix, Prometheus labels all agree. Grep any segment
across any surface finds every mention.

**Cost:** small per-service. Compose entry gets 6 additional lines
(3 labels + 3 env vars). Convention is auto-applied by every future
service.

**When:** dhobley decisions.md entry needs to land first. Then apply
to found-footy in the same PR as the NATS envelope migration (#8) —
they naturally go together.

---

### 13. Directory structure alignment (project-first everywhere)

Discussed today: `~/workspace/dev/<project>/` inverts the
project-first ordering that everything else uses. Restructuring to
`~/workspace/<project>/dev/` would give full consistency at the cost
of a one-time migration + slight loss of "cd to workspace/dev to see
active projects" ergonomics.

**Not urgent.** Cross-project decision, belongs in vedanta-dhobley,
happens on its own timeline. Noting here so it stays in the design-
improvements pool.

**When:** post-Aug-14 minimum. Big-ish migration, low functional
impact, do when there's runway.

---

## Twitter + scale ops

### 14. Parallel video downloads via syndication API (#161 already tracked)

Not a design shift — a known fact. Downloads bypass Firefox entirely
via `cdn.syndication.twimg.com/tweet-result` (no auth, no rate limit).
V-phase's download activity should fire N in parallel per event
without an artificial cap. Python already did this; we just port the
pattern.

**Why note it here:** for the design-improvements pass to remember
that this is a specific pattern change from the archive/ code (which
used yt-dlp per video, serially).

---

## Ingest + Monitor (earlier design era)

Surfaced during the 2026-07-07 to 2026-07-11 workflow-design conversations
that produced Monitor + Ingest as they exist today. Deferred at the time,
still real, some are Aug-14 blocking or near-blocking. Sourced from
[`docs/rebuild/proposals/monitor.md`](./proposals/monitor.md) §3-4 and
[`docs/rebuild/proposals/workflow-audit-2026-07-09.md`](./proposals/workflow-audit-2026-07-09.md)
P1/P2 items that never became tasks.

### 15. Adaptive staging poll tiering

[`monitor.md`](./proposals/monitor.md#1-adaptive-staging-tiering) proposed a
kickoff-proximity tiered poll: within 4h → every 15 min, 4-24h → every
60 min, 24h+ → daily via Ingest only. Currently we run a flat 15-min
cron for all staging fixtures regardless of kickoff distance. That's
~90% of staging API calls hitting fixtures we already know don't need
frequent updates.

**Cost of shipping tiered**: small — `PollStagingBucket` becomes tier-aware
via a filter on `ListStagingForBucketPoll`. Half a day. Ships as pure
optimization; behavior at reasonable poll cadences is identical.

**When**: not Aug-14 blocking, but Aug 14 will see peak fixture load
(top-5 European leagues resuming). API burn under peak = real cost.
Land during a lull.

### 16. Adaptive debounce thresholds for late-game goals

[`monitor.md` §3](./proposals/monitor.md#3-adaptive-debounce-thresholds-deferred)
flagged this: 92-minute goals are common; a 3-poll × 30s debounce means
we spawn Discovery ~90s after the final whistle. Missed clip windows.

Trade-off: 2-poll debounce is more susceptible to false positives (a
transient event that vanishes next poll). Would want telemetry on
mis-report frequency before committing. That telemetry exists now —
`events.debounce_count` history + `event.removed` NATS emission =
real data on how often events drop back to zero after being sighted.

**Cost**: small — one branch in the debounce activity + one config
value + telemetry review. But NEEDS the drop-frequency review first,
otherwise we're tuning blind.

**When**: post-audit, when we can look at real prod drop counts. Aug 14
launch itself is when we'll first accumulate meaningful telemetry.

### 17. Long-postponement handling (PST design gap)

Currently shipped design (per [`fixture.go:70-80`](../../internal/domain/fixture/fixture.go)):
PST is treated as ACTIVE. The design comment explicitly targets "short
delays (15-30 min)" — weather postponements that resume within the same
window. Correct for that case.

**The gap**: long/indefinite postponements. Ligue 1 matches moved 2
weeks later, Bundesliga winter weather-cancellations rescheduled to
midweek, etc. "Treated as active" means we keep polling a fixture
that's effectively dead for days. Wasted API calls; also potentially
confusing state (fixture stuck at api_elapsed=0 forever).

[`monitor.md` §2](./proposals/monitor.md#2-postponed-fixture-handling--new-state)
originally proposed a fourth `fixture.State` value `postponed`. That's
one option. Another: extend the current active state with a
`postponed_since` timestamp — if PST persists >6h, back off polling to
daily; if >30d, prune.

**Cost of state option**: medium — schema migration (`ALTER TYPE
fixture_state ADD VALUE 'postponed'`), domain methods
`Postpone`/`Resume`, Ingest rescan logic to unpause on schedule
change, Monitor filter to skip.

**Cost of back-off option**: small — one timestamp column + one
Monitor filter change. No new state.

**When**: not Aug-14 blocking (short delays work). But once Aug 14
lands and we see a long delay in prod, this becomes P1. Design-
improvements pass should pick between the two options.

### 18. `/fixtures?live=all` API alternative

[`monitor.md` opened by asking](./proposals/monitor.md) whether to poll
active fixtures via `live=all` (single API call, no ID tracking) or by
IDs (current path — couples our state to API state cleanly). Kept the
by-IDs path.

**But**: `live=all` gives us API-native "who's live right now" without
us needing to maintain an active-set. Simpler. Might auto-catch
fixtures we don't know are live (transient IDs Ingest missed, tournament
kickoffs we didn't preload). Could be a fallback belt-and-suspenders
alongside by-IDs, not a replacement.

**Cost**: small — new activity + comparator. Would surface Ingest gaps
as a side effect.

**When**: post-Aug-14. Ingest already catches the vast majority; the
edge cases this catches are rare.

### 19. Static national-team fallback (TOP_FIFA_IDS)

[`workflow-audit-2026-07-09` P1 #5](./proposals/workflow-audit-2026-07-09.md#p1--worth-doing-sooner-rather-than-later):
Python has a static list of ~15 top FIFA national team IDs unioned into
the tracked set. Catches Euros, Copa America, Nations League fixtures
transparently — no per-tournament config edit. Go currently doesn't;
each tournament needs the league ID added to `TRACKED_LEAGUES` env when
it starts.

Aug 14 falls after Nations League autumn window opens. If Nations
League fixtures aren't caught, that's a coverage gap for user-facing
content on a launch weekend.

**Cost**: small — `TOP_NATIONAL_TEAMS` const in ingest config +
union-into-tracked in the tracked-teams cache step. ~2 hours.

**When**: BEFORE Aug 14. Real coverage risk.

### 20. Static UEFA-club fallback for team-fetch failure

[`workflow-audit-2026-07-09` P1 #6](./proposals/workflow-audit-2026-07-09.md#p1--worth-doing-sooner-rather-than-later):
Python has `TOP_UEFA_IDS = [15 clubs]` fallback if the top-flight fetch
completely fails. Go currently returns error → keeps previous cache →
fail-open (no filter) if cache is empty.

Fail-open is BROADER than Python's static list — first-ever cache-miss
followed by API failure means Monitor thinks every fixture is trackable.
Real risk if the first-ever run hits an API outage.

**Cost**: small — hardcoded fallback list + apply on cache-miss path.
~1 hour.

**When**: pre-Aug-14 if we're doing a fresh prod deploy from empty
cache. Otherwise defer.

### 21. Alias `country` metadata passthrough

[`workflow-audit-2026-07-09` P2 #7](./proposals/workflow-audit-2026-07-09.md#p2--polish):
Python passes `league.country` into RAG (alias resolution). Go's
`EnsureAliasPlaceholders` stores only `TeamID + TeamName`. Minor —
RAG can still infer country, just costs one extra API call per team.

**Cost**: trivial — pass country through the placeholder + into the
resolution job. ~30 min.

**When**: whenever the RAG resolution job comes online (see #23 below).

### 22. Ingest retry constants → config

[`workflow-audit-2026-07-09` P2 #8](./proposals/workflow-audit-2026-07-09.md#p2--polish):
`ingestManualIDsMaxAttempts=3`, `ingestManualIDsBackoffInitial=5s`
hardcoded in ingest.go. Workflow-scoped so not a drift risk, but
inconsistent with the just-landed `WorkflowsConfig` pattern (and now
inconsistent with today's Discovery config move via #162).

**Cost**: trivial — same pattern as #162. ~30 min.

**When**: sweep-along with any other Ingest work. Or right now while
the pattern's fresh in my head.

### 23. RAG resolution job — the "TBD design"

Multiple decisions.md entries reference "a separate resolution job
(design TBD) fills placeholder alias rows." That design is still TBD.
Currently placeholder rows exist; something is presumably resolving
them (Wikipedia CirrusSearch + LLM per decisions.md 2026-07-21) but
the ORCHESTRATION shape isn't documented — is it triggered on
placeholder-row insert, on a schedule, on-demand from Discovery?

If it's on-demand from Discovery (which we saw during Miami — Discovery
falls back to TeamName when aliases are unresolved), that's fine but
means Discovery's first attempt for a fresh team runs with poor recall
until aliases fill in. Not ideal.

**Better options** to consider:
- **NATIVE Temporal Schedule** — a daily job resolves all
  `resolved_at IS NULL` rows via the standard resolver
- **On-insert triggered workflow** — Ingest's `EnsureAliasPlaceholders`
  spawns a resolver workflow for each new placeholder immediately
- **Batch on-need pre-warm** — Ingest's fixture-fetch step notes new
  team IDs, batches resolution before Monitor starts polling

**Cost**: depends on which shape. Small on-schedule is easy, on-insert
is medium, on-need pre-warm is medium+ but best UX.

**When**: needs to land by Aug 14 or Discovery's first-hit recall on
fresh teams is degraded. Priority for the design-improvements pass.

### 24. Workflow-audit as recurring practice

The 2026-07-09 workflow audit was a valuable one-time exercise —
cross-referenced current Go vs Python vs plan, produced a punch list,
several items shipped as direct outcomes. Never repeated.

Every 2-3 phases (or every ~200 commits) another cross-reference audit
would catch drift while it's cheap. Especially useful during a big
push like V-phase where the ratio of "code shipping" to "docs updating"
tends to drift.

**Cost**: medium per audit — half day if scripted the same way
(parallel Explore agents per workflow).

**When**: schedule for post-V-phase. Doesn't need to be automated;
just needs to happen.

---

## Tokenizer / normalization

### 25. ~~Revisit dropping non-Latin scripts~~ — RESOLVED 2026-07-24

Raised by the user ("why do we drop things at all?"). **Resolved same
day**: adopted `gosimple/unidecode` and deleted the drop-non-Latin rule.
Non-Latin scripts now romanize (Спартак→spartak, 레알→real) instead of
being discarded; the ≥2-language threshold voting drops romanization
noise (红魔→"hong mo"). Fully dynamic, no per-script special-casing.
See [decisions.md 2026-07-24](../decisions.md). Kept here as the record
of the question; no further action.

### 26. isCamelConcat over-drops Mc/Mac names (McTominay)

Found 2026-07-24 during the unidecode work. `isCamelConcat` drops any
token with an internal lower→upper transition (designed to kill Wikidata
concat forms like `LiverpoolFC`, `ACFFiorentina`). But it also drops
legitimate names: **McTominay** (Man Utd/Napoli), **McBurnie**,
**DeAndre**, any `Mc`/`Mac`/`De`+capital surname written without a space.
Pre-existing (not a unidecode regression), but now more visible since
it's the last non-normalization tokenizer edge case.

**Fix options**: (a) whitelist `Mc`/`Mac`/`De`/`O'` prefixes before the
camelConcat check; (b) only treat as concat if the token also has no
spaces AND is ≥N chars AND matches a known org-suffix — narrow the rule
to actual Wikidata concats. (a) is simplest.

**Note**: unidecode now produces camelCase mashups for CJK
(ヴィッセル神戸→"vuitsuseruShen") that isCamelConcat happens to drop — a
useful side effect. Any fix to (a)/(b) should confirm CJK noise still
gets dropped by the threshold, not rely on isCamelConcat for it.

**When**: not Aug-14 blocking (Scottish/Irish surnames are a minority),
but a real recall hole. Cheap fix (option a).

---

## Not-yet-articulated opportunities (placeholders)

These are things I've mentioned in passing that deserve their own entry
if they matter. Adding as placeholders so future me remembers to think
about them:

- **Twitter service concurrency** — Miami smoke test showed ~15% of
  concurrent search calls to a single twitter instance time out. T/d
  solves this at the scaling layer, but is there also a per-instance
  concurrency semaphore worth adding as belt-and-suspenders?
- **Firefox startup optimization** — can we cut the 10-18s cold-start
  significantly? Preloaded cookie jar, skip-onboarding profile
  template, lighter alternative browser?
- **Discovery + V-phase pipelining** — currently Discovery finishes,
  then V-phase starts. Could V-phase start on the first candidate
  landing and run in parallel with continued discovery attempts?
  Latency vs complexity tradeoff.

---

## How this doc gets used

Input to the follow-up design-improvements pass (planned after audit
lands 2026-07-26). Each entry gets triaged:
- **Adopt now** → becomes a task, sized against Aug-14 runway
- **Adopt post-Aug-14** → task with deferred label
- **Reject with reasoning** → deleted from this doc, reasoning
  captured in decisions.md so we don't rediscover the same idea
- **Needs more thought** → stays here, discussion continues

Update this doc as new observations land — during audit review, during
implementation, during operational surprises. Snapshot on 2026-07-23
timestamps the current baseline; edits should keep entries fresh
rather than mount new dated snapshots (no need for two design-
improvements-*.md files).
