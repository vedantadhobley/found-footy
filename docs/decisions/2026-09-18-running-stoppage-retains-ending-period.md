# Running stoppage retains the ending period

## Decision

FF-097 introduces `vision-evaluate-v4`. A positive, syntactically valid
added-time allowance beside an unlabelled running clock just beyond a period
end supplies stoppage-period evidence. The measured minute stays unchanged:
`46:35 +4` means minute 46 in 1H, not minute 50 or ordinary 2H.

Normalization happens in the parser before evaluation, without the expected
API time. No evaluator exception tries to rescue the requested goal. This
extends the [literal-clock decision](./2026-09-15-clock-fields-preserve-visible-evidence.md)
and preserves the [visible-offset requirement](./2026-09-17-relative-clocks-require-visible-period.md).
The allowance is not itself a half label and cannot authorize a relative offset.

## Recognition and limits

- Apply only after explicit period composition. A visible period, compact
  stoppage form, or conflicting/ambiguous explicit evidence is never overridden.
- Require a positive allowance; absent, zero, negative, elapsed-timer-shaped
  and malformed fields do not supply this evidence.
- Recognize completed clock minutes strictly above each boundary and below
  boundary +15: 46–59 for 1H, 91–104 for 2H, 106–119 for ET1, 121–134 for ET2.
  This is a conservative recognition policy, not a law of football. Fifteen
  keeps adjacent extra-time windows disjoint and avoids assigning an unlabelled
  89:xx pre-announcement to first-half stoppage. It is independent of the
  announced allowance, which is neither elapsed time nor an upper limit.
- Explicit labels continue to support longer stoppage. Outside the recognition
  window, retain the prior conventional interpretation; do not invent a label
  or broaden acceptance to every numeric clock carrying an allowance.
- Clock values exactly on 45/90/105/120 retain their existing behavior.
  Seconds-visible zero-second boundaries may compose with a secondary timer;
  an allowance alone cannot verify a frozen boundary. Minute-only readings
  remain minute-only. Running clocks never add a secondary timer.
- Retain ±1 tolerance, API `elapsed + extra - 1`, any-usable-match admission,
  content gates, and the existing conservative treatment of ET conflicts.

Unlabelled extremely long stoppage, reset clocks without a visible label, early
allowance announcements in extra time, and bad OCR can remain ambiguous or
misclassified. The fields do not identify every broadcast convention uniquely.
Do not claim the recognition window eliminates those limits. Expanding it needs
concrete evidence and negative controls, not a silently larger time allowance.

## Evidence and durability

The [incident and implementation report](../design/audits/running-stoppage-period-2026-09-17.md)
records the pre-fix failures, all-four-boundary tests, saved Gueye observations,
activity-boundary evidence preservation, and before/after production-evidence
replay. The raw model period remains null when the parser infers stoppage;
`ClockReading` separately records the normalized period and its provenance.

New activity results and captured acceptances identify v4. Old v1/v2/v3 evidence
is validated structurally, not rewritten or automatically revalidated. Offline
replay accepts each known version and keeps recorded/current results separate.

No model request, prompt/schema change, database migration, search change,
ranking-policy change or frontend contract is introduced. Production deployment
and historical recovery are separate approved operations. The
[September 18 release](../history/running-stoppage-rollout-2026-09-18.md)
deployed v4; model selection and historical recovery remain unresolved.
