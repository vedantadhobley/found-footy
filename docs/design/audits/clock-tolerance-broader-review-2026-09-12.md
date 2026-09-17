# Broader clock-tolerance review — 2026-09-12 UTC

## Disposition

Do not deploy global zero tolerance from the earlier incident sample alone.
This wider review finds useful goal footage whose sampled broadcast clock is
one minute later than the normalized API target. Tightening the window also
filters non-goal footage; it does not distinguish these two categories.

The [earlier exact replay](./clock-tolerance-2026-09-12.md) remains valid:
four Mastantuono cross-admissions fail at zero. This review adds different
evidence, not more recovered historical model responses. It also exposes
**FF-093**, missing durable accepted-vision evidence, and **FF-094**, a running
timer incorrectly treated as unpinned when an added-time label is present.

No production policy, data, configuration, schema, search or workflow changed.
No new model requests were made. User approval covered read-only investigation
and bounded original-media inspection. Local additions are audit tests and
documentation, not an application fix or deployment.

## Scope and available evidence

The fixed window is completed fixtures with kickoff in
`[2026-08-29T00:00:00Z, 2026-09-12T00:00:00Z)`: 14 UTC dates, 166 fixtures,
643 events and 34,903 candidate rows. These are retained rows, not independent
clips or correctness labels. The inventory began at 04:04 UTC on September 12;
the four production application processes reported `bad5ffc`.

Read-only repeatable-read SQL used a 15-second statement timeout. A first
inventory tested the wrong JSON key, `frames`; its zero counts are discarded.
The corrected query uses the actual `frame_observations` field:

| Candidate outcome | Rows | Rows retaining raw frame observations |
|---|---:|---:|
| Promoted | 909 | 0 |
| Superseded | 471 | 0 |
| Duplicate | 7,486 | 0 |
| Rejected | 21,514 | 4,961 |
| Failed | 4,523 | 0 |

Rejected rows without frames can have failed other gates; this table does not
claim they all reached vision. Exact followers can share one validation.

Temporal's live namespace has 86,400-second retention and both archival modes
disabled. An ordinary Cristante 39′ workflow completed September 10 at 17:42 UTC
(`event-bb876e77-a911-4242-80af-1681bc51fe18`) could no longer return history:
`sql: no rows in result set`. The rejected-only durable evidence cannot provide
an unbiased retrospective replay of accepted candidates.

## Share-level screen and preselected media

The SQL projection returned 1,380 shares across 142 fixtures. Of 975 verified
shares, 928 have positive normal-time API minutes and no added time. Compare
their stored `extracted_minute` with the **current** event's `minute - 1`:

| Stored minute offset | Shares |
|---|---:|
| Exact | 739 |
| One earlier | 68 |
| One later | 121 |

The 189 edge shares cover 124 events. This is **not** 189 strict rejections:
the stored minute is the first qualifying reading, later samples may be exact,
and event times may have changed since validation. Superseded shares are also
included in that screen. Never use these counts as a false-positive rate.

113 edge shares remain active with unremoved events. Restrict to goals, no
other fixture event within two minutes, a null reclamation marker, and original
objects no larger than 20 MB: 76 eligible shares. A null marker is only a
selection filter, not proof an object exists. For each offset, sort by
SHA256 of `share_id` and select four, skipping any fixture already selected.
Process the earlier edge first. Tweet text and visual content do not affect
selection. Recomputing this rule reproduced all eight selected asset IDs.

All eight exact originals were fetched successfully: 50,967,743 bytes, with
size and MD5 matching the database. Downloads used the original object keys,
not share redirects that might resolve to a replacement. No other originals
were fetched. Active shares may still be hidden by popularity filtering; this
is not a claim that all eight were exposed in the frontend.

Offline ffmpeg extracted native frames at 25/50/75% of stored duration and
one-frame-per-second storyboards. Same fractions as production, but regenerated
from millisecond duration using this local ffmpeg build: these are not asserted
to be byte-identical to the old model's JPEG inputs. The agent inspected the
frames and sequences; the user has **not** confirmed these visual labels.

## Eight visual reviews

The outcome column runs **main-timer-only, agent-read controls** through the
actual evaluator. It uses neutral soccer/screen votes and current API time.
It does not recover historical OCR, classifier votes or original API inputs.
`V` means verified and `R` rejected. Missing clocks stay missing.

| Stored event | Sampled main clocks | ±1 → zero | Visual finding |
|---|---|---|---|
| Neto 14′, Chelsea–Brighton | 12:01, 12:07, 12:12 | V → R | Defensive clearance/counter ends in a midfield challenge. No goal in the reviewed sequence. |
| Farrington 27′, Dallas–Kansas City | 29:53, none, none | R → R | A goal and replays, but clock and source attribution conflict with the stored event/summary. Unresolved, not a measured strict-policy improvement. |
| Sancet 90′, Athletic–Atletico | 88:58, 89:06, 89:13 | V → V | Buildup, goal and celebration; score changes 2–0 to 3–0. Later exact samples preserve acceptance despite stored minute 88. |
| Gibbs-White 70′, Liverpool–Forest | 67:53, 68:08, 68:22 | V → R | Penalty-award incident, referee discussion and foul replays, not the scored penalty. |
| Fernández 45′, Sociedad–Espanyol | 45:17, 45:25, 45:33 | V → R* | Goal replays with score already 1–1. Running broadcast clock continues over replay footage. |
| Isak 9′, Ipswich–Liverpool | 09:14, 09:22, 09:29 | V → R | Multiple views of a goal, score already 0–2 while the replay clock advances. |
| Bischof 24′, Osnabrück–Bayern | 24:14, 24:15, 24:17 | V → R | Short goal-action/celebration cut; displayed score is already 1–2. |
| Havertz 25′, Arsenal–Chelsea | 25:15, 25:18, 25:21 | V → R | Brief goalmouth aftermath followed by a sideline reaction, not a focused scoring sequence. |

*Fernández also visibly has a `+1` announcement. Including that field exposes
the separate FF-094 parser behavior below. The main-timer-only row is not a
claim that the historical model omitted the announcement.

Six of eight clock-only controls change: three contain goal footage consistent
with their source descriptions, two show other actions, and one is
reaction-dominated. One remains verified; one already contradicts the current
baseline when its clock is reread accurately. These are **case classifications,
not population accuracy estimates**. Player/event identity was not established
from faces; source text, teams, sequence and score provide corroboration, not
an authoritative event label. This does not measure whole-event video loss.

Farrington deserves separate follow-up under FF-003: the retained minute is
25, while the first regenerated frame clearly reads 29:53 and DAL 2–2 SKC.
The stored source describes Petar Musa. Exact MD5 proves the inspected bytes
match the original asset, but expired raw observations prevent deciding whether
the fault was OCR, event metadata, or another attribution path. Do not invent
a historical `29 → 25` model error from this summary alone.

## FF-094: added-time label discards a running clock

The Fernández samples expose a concrete parser branch. In
[`parseFrameClock`](../../../internal/domain/vision/clock.go), any valid `Added`
field without an already parsed stoppage clock sets `ExactMinute=false`.
It does not establish that the main timer is frozen. Thus the running sequence
`45:17, 45:25, 45:33`, each with `added: "+1"`, loses usable minute evidence.

The offline characterization proves both effects, at tolerance one **and** zero:

- Against the actual stored API 45′/no-extra input, all three readings reject.
  Without the `Added` fields the same main clocks verify at tolerance one.
- Against a **constructed** API 45+4′ target, the readings verify through
  `frozen-boundary stoppage, period match (minute unpinned)`, although the
  expected minute is 48 and the visible main clock is 45:xx.

This is a confirmed current-code defect, not proof that this exact faulty
field combination reached production for Fernández. A fix must distinguish
running minute evidence from genuinely frozen/ambiguous timers. Announced
added time is not an elapsed sub-timer. Changing global tolerance cannot fix
this branch; preserve true frozen-clock and reset-period regressions.

## FF-093: accepted vision evidence expires

[`onVisionDone`](../../../internal/workflow/event_pipeline_validation.go)
persists full raw frames and normalized clocks for rejections. Accepted clips
instead enter placement; its
[`placementCandidates`](../../../internal/workflow/event_pipeline_placement.go)
records asset/winner identity and a compact verification result. Shares retain
one matched minute, not the original three observations. Once history expires,
the successful validation can no longer be faithfully replayed.

This is an auditability gap, not evidence that successful processing or Temporal
durability failed. Proposed follow-up: retain one bounded validation record per
actual evaluation, associated with its event/MD5 and exact followers. Preserve
original expected API time, raw frame fields, verdict and evaluator/model
provenance. Do not duplicate JPEGs or full workflows on every tweet row, rewrite
old decisions from current event data, or make a new model call a substitute for
the original result. Schema and retention design remain unimplemented.

## Recommendation

1. Fix the independently reproduced running/frozen-clock parser defect.
2. Retain accepted validation evidence before measuring a wider policy change.
3. Keep global ±1 unchanged for now. Compare stricter admission with useful
   replay retention using these controls and future complete observations.
   The decision is not just whether to tolerate provider mistakes: a replay's
   displayed clock can refer to broadcast time rather than the pictured goal.
4. Keep FF-003 semantic attribution and FF-081 replacement/coverage separate.
   Neither exact-minute matching nor three agreeing clocks proves which action
   is pictured or that a longer clip can safely replace another.

No adaptive allowance, replay detector, extra model sampling, or fallback policy
has been accepted or implemented by this review.

## Durable identities and reproduction

The public source links below identify stored candidate provenance; they were
not freshly fetched from X. Original bytes, not current redirected share media,
were inspected. Each local filename is `<asset_id>.mp4`.

| Event / fixture | Share / source | Original asset ID | MD5 |
|---|---|---|---|
| Neto / 1557379 | [s_e9beb5b36095](https://x.com/AyindeRM/status/2094052115303866822) | `754dda66-426d-5a0d-9b8b-1f6c393474e2` | `f5b97dceefca258bcc259d513fc465e6` |
| Farrington / 1490445 | [s_4943011ee5e0](https://x.com/ocrismoraes/status/2096407751471550516) | `3ed10aa6-538c-5e76-9909-a8efbcbcaf8b` | `ea623292057d29b03e2fcff076d69b33` |
| Sancet / 1570364 | [s_a44d90471f8b](https://x.com/ESPNDeportes/status/2096271423207580114) | `acb89797-5a06-5bc9-bf46-34d278698641` | `a87472e4ceebabd5ff86b47c793c5428` |
| Gibbs-White / 1557383 | [s_103cc4232f61](https://x.com/SleeperFootNews/status/2093687688394137683) | `23ea9226-fa11-50ad-aa0b-52e0e3f5167c` | `63835749fbe9422e6b1a4203c1ddad3f` |
| Fernández / 1570361 | [s_63b1332db2fb](https://x.com/futballcenta/status/2093758250348949878) | `699b6ca6-e4a9-5211-b521-201cfafaefef` | `91d77d1b9f8b2350bca1a254bc3149cb` |
| Isak / 1557393 | [s_6e840010ed92](https://x.com/fran_agyare/status/2095953288533344550) | `4cc2a40e-7469-5d1a-a2a2-67a36d8006b6` | `ddd7dade7a1c2f3c53d01fac4c6272cc` |
| Bischof / 1550705 | [s_b3c808bad9e1](https://x.com/fujolenses/status/2095230234152829361) | `135a9cd8-15f1-5357-b45f-c848e7667de0` | `511002efc40f9f8fba0c2a556a62bd29` |
| Havertz / 1557387 | [s_73cc9d1daadb](https://x.com/mo_rzik7/status/2096629098856558635) | `c079cdcf-6d7f-5778-8395-d21f6e4d801c` | `74e6cfbb18077fb1498cfca68922668f` |

The executable [review controls](../../../scripts/audit_clock_tolerance/review_test.go)
are deliberately separate from the 108-row saved-history corpus. Audit and
domain-vision tests pass, including the eight visual controls and the current
FF-094 characterization, also with `-race -count=3`. `go vet` passes and pinned
golangci-lint reports zero issues for the audit package. Changed-document local
links and `git diff --check` pass. Full application/integration gates were not
run because no runtime or schema code changed. These tests preserve evidence of
the bug, not desired future semantics; update that characterization when
implementing its fix.

Local evidence is under `scratch-audit-2026-09-12/clock-tolerance/`: fixed-window
SQL/results, selected manifest, verified fetch results, originals and sheets.
Offline extraction used the existing benchmark image
`sha256:c36f5c5cf7f44c74cc5064ad9a927b4160e5b511e81a65d8f362b959f8c17972`,
with no network and explicit CPU/RAM caps. No media or secrets are added to Git.

```text
broad-inventory.ndjson       f3587939c6703305c6b1b8bfa658f256122df9b4347cbad286193d1daf27a054
broad-shares.ndjson          b830d6349af90f246e8f2fda8b1ad1429090079a7f9e764a4f81220a4974c15b
ordinary-media-targets.json  09bc32446deee71d299fbc6ce38363df84e4ffe932a1188f5fc03c76eb8f13b7
media-fetch-results.json     35025a6e85947a987f6127c7c3e21465d430d65cebcfd45f03efb7c5ee5d7a98
```
