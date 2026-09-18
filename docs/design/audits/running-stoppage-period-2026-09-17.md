# Running stoppage clocks assigned to the next period

## Scope and disposition

Read-only production diagnosis for
[FF-097](../../todo.md#ff-097--running-stoppage-clocks-inherit-the-next-period).
The September 17 Malaga–Villarreal incident establishes a real false rejection.
Historical SQL screens identify the same evidence pattern, not independently
human-labelled footage or a count of unique lost videos. The initial diagnosis
changed no runtime policy. The [local implementation](#implementation-and-verification)
below followed user approval; production data, search and model calls remain
unchanged.

## Gueye 45+2′

- Fixture `1570390`, event `5956a72d-75cc-4f7e-bf54-bdf1afc83cd7`.
- Discovery completed all fifteen usable searches with zero unavailable
  attempts, ending `candidates_no_assets`. Its 38 candidate posts ended in
  21 clock rejects, ten metadata rejects, and seven forbidden CDN downloads.
- Four posts have saved clocks inside the expected completed-minute window:
  three read `46:35/37/39`; another reads `46:46, 47:00, 47:30`.
  Each has `added: "+4"`, no explicit period, no secondary timer, unanimous
  soccer votes, and no screen-recording votes.
- The expected value is correctly `45 + 2 - 1 = 46`, period `1H`, tolerance
  ±1. The v3 parser instead records exact minutes 46/47 with inferred `2H`.
  Evaluation rejects the period mismatch.
- A public copy of [TheGoalsZone's post](https://x.com/TheGoalsZone/status/2100681627579879691)
  was downloaded and inspected. Its scorebug visibly reads `46:35`, `+4`,
  `MGA 1–1 VIL`. The saved model observation is accurate for that frame.
  The failure is downstream interpretation, not invented OCR seconds.
- The four candidate IDs are `82d11826-e163-4b07-879c-fbb84de9c062`,
  `b5d0a8a3-8241-4efc-9606-e854d4b5a937`,
  `65755e8d-6b8d-437e-ab1d-2ec2a64dcbf6`, and
  `43bfea20-59f8-4ed2-8a04-6bfbce00dfdb`. They are not established as four
  distinct byte variants.

Other clock rejects include 48:xx and 39:xx footage. Correcting the period does
not put those observations inside this goal's 45–47 minute window. This is not
authorization to admit all rejected candidates.

## Origin and adjacent boundaries

Commit `a67c3de` introduced the period-aware Go evaluator on July 28. It applies
`periodOf(base)` to a running broadcast clock as well as to API elapsed time.
The latter is a period base for stoppage events; the former may already include
elapsed stoppage. These are different inputs. The defect was therefore present
at the August 15 production cutover, not introduced by September 17's release.
The archived Python validator compares numeric minutes without this period
guard; this particular rejection is a Go rewrite regression.

The current parser still initializes plain clocks through `periodOf`. An
explicit period corrects that inference, but a running clock returns before
the allowance is considered: allowance handling only follows a compatible
frozen end boundary. FF-094 repaired minute arithmetic and precision without
repairing this period inference.

An offline probe of the unchanged evaluator, with three identical soccer
observations, allowance `+4`, no secondary timer and ±1 tolerance, yields:

| Clock | Expected event | No period label | Explicit correct period |
|---|---|---|---|
| 46:35 | 45+2′ | `2H`, rejected | `1H`, verified |
| 92:35 | 90+3′ | `ET1`, unverified | `2H`, verified |
| 106:35 | 105+2′ | `ET2`, unverified | `ET1`, verified |
| 121:35 | 120+2′ | `ET2`, verified | `ET2`, verified |

ET-involved conflicts currently soft-keep rather than hard-reject. They can
therefore affect the verified bucket, ranking and visibility instead of simply
losing all footage. There is no later enum after ET2, so the last example
escapes this specific misclassification; it still needs regression coverage.

Existing tests cover frozen main clocks plus elapsed secondary timers, compact
stoppage forms, explicit period labels, and allowances on 45:xx/90:xx. The
running-clock auxiliary-field tests supply explicit periods. They omit the
incident's crucial combination: past the boundary, allowance present, period
absent. Historical unchanged-verdict replay was not a labelled correctness
test and did not expose the missing positive case.

## Historical screen

Read-only SQL on September 18 UTC inspected retained candidates beginning
August 15. The predicate requires the exact clock-mismatch rejection reason,
expected period `1H`, expected minute at least 45, and at least one raw frame
with an `MM:SS` minute above 45 within ±1 of the stored expectation, a numeric
allowance, no explicit period, and no secondary timer. Absent/legacy-shaped
frame arrays are excluded. This is a bounded signature screen, not an estimate
of every affected event or of semantic validity.

| Fixture | Event | Candidate posts | Ever-created share rows |
|---|---|---:|---:|
| 1490398, Sporting KC–St. Louis | E. Lowen 45+2′ | 4 | 1 |
| 1490415, Vancouver–Dallas | E. Ocampo 45+6′ | 4 | 0 |
| 1570346, Elche–Barcelona | K. Adeyemi 45+3′ | 28 | 4 |
| 1627558, Monterrey–Chicago | V. Guzman 45+4′ | 6 | 2 |
| 1627561, Club America–Columbus | B. Rodriguez 45+5′ | 13 | 0 |
| 1550106, Sassuolo–Torino | Pietro Comuzzo 45+2′ | 4 | 0 |
| 1550104, Monza–Udinese | Jurgen Ekkelenkamp 45+3′ | 2 | 0 |
| 1636206, West Ham–Fulham | Pablo Felipe Pereira de Jesus 45+2′ | 17 | 0 |
| 1570390, Malaga–Villarreal | Pape Gueye 45+2′ | 4 | 0 |

Total: 82 candidate posts across nine events; six have no share history.
The earliest matching row was discovered August 20 at 01:17:05 UTC. Shares
created for older events may have since been reclaimed; current active-share
counts must not be mistaken for original discovery failures. Historical
footage, exact-byte identity and replay availability need separate review.

## Proposed implementation boundary

1. Separate API period-base classification from broadcast-period inference.
   Normalize the main clock, explicit period, allowance and secondary timer
   together before evaluation. Do not hide another exception in the evaluator.
2. Recognize running stoppage evidence without changing its numeric minute:
   `46:35` remains minute 46, never 50 because an allowance reads `+4`.
   The allowance is neither elapsed time nor a hard ceiling on actual play.
3. Cover 45, 90, 105 and 120 together. Preserve explicit period precedence,
   compact/frozen forms and supported reset-clock interpretations. Contradictory
   or genuinely ambiguous evidence must not acquire a confident period from
   the API target. Do not blindly relabel every minute above 45 as first half;
   pre-boundary allowance displays and reset clocks need negative controls.
4. Add the actual Gueye observations and historical examples as regression
   fixtures, plus cross-period wrong-event controls and allowance overrun cases.
   Retain the recent Yamal rejection, no invented seconds, and no period-only
   frozen-clock verification. Keep ±1 and the existing any-match rule unchanged.
5. Replay saved accepted and rejected evidence; inspect all changed categories,
   not just newly admitted candidates. Bump evaluator provenance and keep old
   evidence immutable. No extra inference, database migration or frontend
   contract is expected for this parser work.
6. Deployment and targeted historical recovery remain separate approvals.
   Recovery must use normal validation/deduplication, not force-promote rows.

This was the proposed boundary. The implementation below records the actual
recognition rule and its limitations, not a solution for every convention.

## Implementation and verification

Local v4 applies allowance-based running-stoppage normalization before the
existing frozen-clock handling. Main-minute arithmetic, API expectations and
the evaluator's tolerance/content/admission rules do not change. The
[decision](../../decisions/2026-09-18-running-stoppage-retains-ending-period.md)
defines the strict post-boundary recognition windows and limits. In particular,
unlabelled recognition is bounded independently of announced allowance; explicit
periods still support longer play and remain authoritative.

The new positive tests failed against v3 before implementation. V4 passes all
four period families, both Gueye observation sequences and wrong-half controls.
The activity test verifies captured evidence retains raw `period: null` while
the separate normalized reading records 1H. Old v3 acceptances remain immutable.

A new read-only snapshot exports 725 accepted records and 10,274 rejected
records with frame observations. Both the deployed v3 source and local v4 run
the offline replay tool against identical inputs. Exclusions match: four manual
evaluations, 2,921 rejects without retained clock expectations, and 225 rejects
whose current event times no longer reproduce the retained expectation.

| v3 → v4 comparison | Compared | Changed outcomes |
|---|---:|---|
| Accepted evaluations | 721 | Eleven unverified → verified |
| Rejected candidate evidence | 7,128 | 63 rejected → verified; one unverified → verified |

The second row's unverified baseline is v3's offline result for an originally
rejected record; the production row itself was never rewritten. All 75 changed
results concern stoppage-time events. There are no accepted-to-rejected or
verified-to-unverified changes between v3 and v4 in this included sample.
Existing discrepancies between old recorded policy and deployed v3 are not
credited to this fix. This replay is not a visual truth label or a public
graph/category/popularity projection.

All four Gueye posts verify. The first-half historical cases contribute 63
new verifications across seven events. The other nineteen posts from the
earlier 82-signature screen belong to Guzman and Rodriguez: their saved
expected minutes are respectively 49 and 50, whereas the current event fields
normalize to 48 and 49. They remain excluded pending explicit historical
review rather than combining a parser change with a silent expectation change.

The 52-response archived benchmark retains all original v2 observations. The
four v3 unsupported-offset rejections stay rejected. V4 additionally changes
the two Maitland-Niles `95:xx +5` observations from unverified to verified;
the remaining evaluations match their earlier expectations.

Verification passed the full source-package short test suite, affected race
tests, source-package vet/build and the pinned linter (zero issues). Ignored
scratch programs are excluded from source-package gates. No database schema
changed; no production workflow, candidate or media repair ran.

Artifacts remain under the gitignored
`scratch-audit-2026-09-18/gueye-45plus2/` directory. SHA-256:

- `evidence.ndjson`: `6cb1e1e453eb43587102fe278c3a7f6c344f05018c5dd6972e75ccaecef34d7f`
- `v3-replay.json`: `e5eac4f2711117c0b61dac088ac0a2fd2b06645baa3c60266e7066aa70412741`
- `v4-replay.json`: `fd20285d1c139ac38d61938f6e17cccdea8fa62ab35857265bee656908e65bc8`

Production remains v3. Deployment and historical replay need separate approval.
