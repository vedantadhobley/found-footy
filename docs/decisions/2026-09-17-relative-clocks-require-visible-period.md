# Relative clocks require visible period evidence

## Decision

`vision-evaluate-v3` removes the unlabelled relative-clock soft-keep fallback.
The expected event minute cannot supply a missing period label to rescue a
readable mismatching clock. This supersedes that part of FF-057 and includes
the existing [FF-094 normalization](./2026-09-15-clock-fields-preserve-visible-evidence.md).
Deployed as a2a6830 on September 17 after explicit approval; the
[rollout record](../history/clock-validation-rollout-2026-09-17.md) retains gates
and the remaining natural-event validation boundary.

The [Barcelona investigation](../design/audits/period-label-recognition-2026-09-17.md)
found correct `44:48/51/53`, with no period label, admitted for Yamal's 89′
goal by guessing `44 + 45`. The video was his first-half penalty miss.
An unverified verdict still permits public display; it is not rejection.

Remove the same hypothetical offsets for H2, ET1 and both ET2 conventions.
Explicit period observations continue to normalize supported reset clocks.
For example, `05:25` with observed `2H` means minute 50; without that label
it is minute 5, not an alternative minute 50. A genuine reset-clock broadcast
whose period label is absent or unreadable can now reject. That is preferable
to assuming the requested event proves which half the footage shows.

## Boundaries

- Ordinary continuous clocks do not require an explicit half label.
- The existing canonical period enum still represents visually recognized
  labels; this is not a literal `2H` text requirement. No prompt or schema
  change from the period-label experiment is included.
- Main clock, allowance and elapsed secondary parsing stay unchanged from
  FF-094. Added time is not a period label.
- No-clock admission, explicit display ambiguities, ET boundary handling,
  stoppage OCR leading-digit rebasing, ±1 tolerance, content votes and the
  any-usable-match rule remain separate and unchanged.
- No extra inference, dependency, configuration, database migration, search
  change or ranking rule is introduced. Wrong-match footage with a matching
  clock, including Jesus's recovered clip, remains an open FF-003 problem.

## Evidence and rollout

New evaluations record v3. Keep v1/v2 evidence and completed Temporal activity
results immutable; exact-byte reuse does not revalidate an old acceptance.
Deployment alone therefore will not remove existing wrong clips. Historical
repair requires separate review and approval.

The new regressions failed before removal and pass after it. Saved-response
replay changes only the two Yamal and two unlabelled-control results among
52 September 17 benchmark responses. The September 15 corpus has no incremental
v2-to-v3 changes across 159 natural accepted records, 3,429 clock rejects and
108 history observations. These counts are overlapping evidence rows, not
distinct footage or a correctness rate; see the linked investigation's
[implementation follow-up](../design/audits/period-label-recognition-2026-09-17.md#implementation-follow-up).
