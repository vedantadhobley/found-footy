# Event scores follow the observed timeline

Status: implemented; production rollout pending. The Control adapter change is
a separate slice in the same planned release. Production has not changed.

## Evidence

Málaga–Real Sociedad (`1570418`) exposed two normal asynchronous transitions on
October 18. Retained provider mutation receipts and event detection/removal
audits show:

| UTC interval | Fixture score | Surviving event count | Cause |
|---|---|---|---|
| 17:57:00–17:58:00 | 2–4 | 3–4 | Scorer corrected from Ángel Recio to Pablo Martínez; both generations survived during removal debounce |
| 18:01:30–18:02:00 | 2–5 | 2–4 | Aggregate score preceded Guedes's event by one poll |

The old presentation parity check returned null score context for every goal
during either interval. That selected the consumer's team-only heading. Earlier
goals lost their display despite having unchanged event facts.

## Decision

The fixture scoreboard and event timeline are independently arriving facts.
Neither gates the other's public projection. Derive provisional goal boundaries
from the surviving stored event timeline, ordered by match clock, starting at
0–0. Do not consult the aggregate score to fill, suppress, offset or certify
those boundaries. Missing aggregate scores are independent too.

Late insertions, removals, clock corrections and scorer replacements recompute
the current timeline. Temporary event-derived scores may disagree with the
fixture, including briefly counting overlapping replacement generations. This
is explicitly accepted provisional behavior, not a promise that the complete
real match chronology has been observed. No prior-score cache or invented
placeholder event is added.

Keep genuinely unknown boundaries null:

- Multiple surviving goals at one minute/extra value have no retained relative
  order. Suppress those individual boundaries; their known-team aggregate still
  advances later goals.
- A surviving goal with an unassignable team makes its own and subsequent
  boundaries unknown. Earlier rows remain usable; do not guess its beneficiary.
- Removed goals never advance the live score or create ambiguity for a surviving
  same-clock goal. A directly requested removed goal retains a tentative boundary
  only when its own match-clock placement is unambiguous.

This supersedes only the aggregate-score gate in the
[original producer-presentation decision](./2026-09-18-public-event-presentation-is-producer-owned.md).
Read-only projection changes do not alter event matching, debounce, VAR cleanup,
provider mutation protection, fixture-completion rules, ordering, or recency.
Existing provider receipts, event audits and integrity/completion diagnostics
remain the evidence for disagreements; do not emit warnings on every read for
ordinary lag. A scoreboard lead is not proof an event will arrive shortly.

## Consumer handoff

1. Keep consuming existing `kind`, `presentation.team_side`, `score_before` and
   `score_after`. No new DTO field or endpoint is required. Non-null scores are
   current event-derived context, not certified agreement with the scoreboard.
2. Goal rows always use the same scoreline layout. For null context, retain that
   structure with an unknown value; do not switch to a team-only heading.
3. Never substitute the fixture's current/final score, sort goals to reconstruct
   chronology, or infer team side from provider IDs. Null is not zero.
4. Keep fixture scoreboard rendering independent. Preserve normal dirty-signal
   handling, targeted event replacement and missing-parent recovery.
5. Test null/non-null transitions without layout switching, including scorer
   correction and same-clock ambiguity. Non-goal event layouts are unchanged.

The backend is wire-compatible with the existing consumer and can roll out
first. The consumer layout change remains owned by Vedanta Systems. No schema,
NATS contract, production data repair or historical media replay is needed.

## Regression coverage

Domain tests cover aggregate-first, event-first, unknown aggregate, the Málaga
replacement/lag sequence, late earlier goals, VAR removal, equal-clock ambiguity
and unassignable-team boundaries. API tests require identical event context in
window snapshots, targeted fixture reads, search and targeted event reads while
preserving the independent fixture score. A removed same-clock target cannot
change its survivor's projection.

Both modules passed `make check-short`. Focused race tests passed for event
presentation, the read API and monitor activities. Documentation links and
`git diff --check` passed. No production data or service was changed.
