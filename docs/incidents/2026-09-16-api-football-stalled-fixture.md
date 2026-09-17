# 2026-09-16 API-Football stalled Barcelona fixture

Point-in-time evidence for
[FF-075](../todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state).
The [provider-health and mutation-protection proposal](../design/proposals/provider-integrity-circuit-breaker.md)
owns the design follow-up. This record does not authorize implementation or
production repair.

## Outcome and limits

Barcelona–Racing Santander, fixture `1570385`, was still reported as `2H`,
65′, 4–2 at **21:23:43 UTC / 17:23:43 EDT**. The 65′ goal had first reached
Found Footy about twenty minutes earlier. The user reported that play had
advanced and another goal had occurred; our direct provider checks still
contained no later goal.

The API was reachable and other fixture feeds progressed. This supports a
stalled fixture feed, not a complete API outage. It does not identify the
vendor's internal fault or prove that every other fixture was current.
Recovery time, final missing-event inventory, and eventual clip loss were not
established during this investigation. Twenty minutes is an incident interval,
not an accepted detection threshold.

Checks were read-only: production logs/database inspection and bounded direct
API requests. Production ran `8c836e0f41cc0e168ca4fe0ff3076dfca8c56793`.
No service restart, fixture mutation, event replay, or deployment occurred.

## Evidence timeline

Times below are UTC on September 16. These are normalized findings from the
investigation's query outputs, not a continuous raw-response archive.

| Time | Observation |
|---|---|
| 21:03:30.297 | Stored `first_seen_at` for Yassir Zabiri's 65′ goal, event `5a89fca4-6ea9-4949-acea-4e27bc6c8423`. It reached debounce 3 and launched downstream discovery. |
| 21:13:20 | Both `/fixtures?ids=1570385` and `/fixtures?id=1570385` returned HTTP 200, empty `errors`, `2H`, 65′, 4–2. Requests took about 216–224 ms. |
| 21:13:20 | `/status` reported an active subscription and 2,722 requests used against a 7,500-request allowance. No quota-exhaustion response was observed. |
| 21:14:18 | `/fixtures?live=all` also reported Barcelona at 65′, 4–2. Other fixtures provided the comparison sample below. |
| 21:16:00.023 | Barcelona's database `last_polled_at` advanced while its stored clock and score remained 65′, 4–2. |
| 21:16:02–03 | The by-ID fixture response remained unchanged; `/fixtures/events?fixture=1570385` also returned HTTP 200 and no goal later than 65′. |
| 21:23:42.878 | `/fixtures?ids=1570385` again returned HTTP 200, empty `errors`, 65′, 4–2, with no later goal; latency 235 ms. |
| 21:23:43.014 | `/fixtures?live=all` agreed on Barcelona, while other fixtures had advanced; latency 136 ms. |

The first-seen timestamp establishes when we learned of the 65′ goal. It does
not establish the exact moment the provider clock stopped advancing. Direct
samples establish the unchanged feed from 21:13 through 21:23.

### Fixture-level versus provider-wide evidence

| Fixture | 21:14:18 | 21:23:43 |
|---|---|---|
| Barcelona–Racing Santander (`1570385`) | `2H`, 65′, 4–2 | `2H`, 65′, 4–2 |
| Coquimbo Unido–Huachipato (`1505509`) | `2H`, 83′, 0–1 | `2H`, 90+3′, 0–1 |
| Zamora FC B–Fundación Lara Deportiva (`1635924`) | `2H`, 82′, 0–2 | `2H`, 90+2′, 0–2 |
| Deportivo Cuenca Juniors–San Antonio (`1632429`) | `1H`, 45′, 0–0 | `HT`, 45+1′, 0–0 |

Multiple endpoints agreeing on Barcelona is not independent corroboration:
they can share the same stale upstream data. Fresh HTTP dates and successful
responses establish request service, not freshness of football facts.

## Local processing and detector behavior

Sampled active-poll cycles continued at the normal 30-second cadence, fetching
all 11 requested fixtures with no fetch errors. Existing Zabiri discovery
continued; a clip promotion and event update were observed around 21:11:30.
This was not evidence that all downstream work had stopped. The unreported
goal could not enter debounce or discovery before the provider supplied it.

The deployed FF-075 evaluator did **not** flag Barcelona:

- Its [comparison facts](../../internal/domain/providerintegrity/types.go)
  have no observation-age or last-progress timestamps.
- Its [rules](../../internal/domain/providerintegrity/evaluate.go) compare
  stored and observed identity, phase, clock, score, populated fields, and
  confirmed-event presence. An unchanged clock does not regress. An event
  never observed cannot count as a disappearing confirmed event.
- `trusted` means no implemented regression rule fired. It does not mean
  fresh, complete, or independently verified.
- [ActivePoll](../../internal/workflow/active_poll.go) aggregates advisory
  verdicts after reconciliation. The wire checks enforce response validity,
  but semantic quarantine and mutation enforcement have not shipped.

The sampled shadow warnings concerned Cincinnati fixture `1490439`
(`populated_field_cleared`), with a `trusted` global policy and zero missing
confirmed events. They were not a Barcelona freshness alarm. Repeated warnings
from one old fixture must not be mistaken for multiple independent failures.

No destructive loss caused by this freeze was established. The August 29
incident involved regressing data and destructive reconciliation; this one
exposes a separate inability to detect stale but structurally valid data.

## Design findings to carry forward

FF-075 must separate **health assessment** from **mutation protection**:

1. Assess availability/contract validity, freshness, and semantic consistency
   separately. HTTP 200 is not proof of live coverage, and an absent warning
   is not proof of health.
2. Keep per-fixture temporal evidence: successful observation time, meaningful
   progress by field, and the retained baseline used to judge regressions.
   Repeated identical successful responses must not reset progress age.
3. Interpret progress by phase. Active play, halftime, extra-time breaks,
   shootouts, postponement, interruption, and terminal states have different
   expectations. No goals for twenty minutes is normal; a running match clock
   frozen for twenty minutes warrants a freshness warning. An unreported
   interruption can still make that warning uncertain.
4. Derive provider-level degradation from transport/coverage failures and
   temporally correlated fixture evidence. One stalled fixture must remain
   distinguishable from a broad outage. Do not count repeat observations of
   the same unresolved cause as new affected fixtures.
5. Map the evidence to an explicit write policy. Continue polling and preserve
   established facts. Isolate suspect fixtures; do not block credible updates
   for unrelated fixtures merely because one feed stopped. Freshness warnings
   and proven invalid payloads need not have identical consequences.
6. Recover against the actual cause: request success for transport failure,
   credible renewed progress for staleness, and restored facts or supported
   corrections for integrity failures. Three identical HTTP-200 observations
   cannot establish recovery from a frozen feed.

There is also a downstream recovery boundary to audit. The fixed Twitter
window is initialized from `events.first_seen_at` minus the configured lookback
in [search-window initialization](../../internal/activity/discovery/search_window.go).
[EventWorkflow](../../internal/workflow/event.go) defaults that lookback to
three minutes when no positive configured value is supplied. A goal first
reported after a long provider delay could therefore exclude earlier real
tweets. This is a code-derived risk, not measured clip loss in this incident;
define a bounded recovery policy rather than silently widening every search
or inventing an exact wall-clock goal time from its match minute.

## Resume checklist

- Revise FF-075's health model before implementing its existing circuit-state
  table verbatim. Keep the shipped wire validation and pure regression rules.
- Set phase-aware freshness thresholds, correlation scope, evidence retention,
  and cause-specific recovery criteria; none are settled by this incident.
- Pin this frozen-feed case beside the August destructive-regression cases,
  normal pauses, partial event-feed lag, and isolated-versus-global controls.
- Decide how delayed first observations affect Twitter search coverage.
- Specify durable evidence and pre-write enforcement, then implement and
  validate them. A new service or Temporal workflow is not required merely
  to assess observations from the existing poll loop.
- Record eventual recovery separately if inspected later. Do not present this
  bounded investigation as proof that the provider remained stalled afterward.
