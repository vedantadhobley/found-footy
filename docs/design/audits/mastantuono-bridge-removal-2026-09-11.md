# Mastantuono second-goal removal by a losing bridge — 2026-09-11

## Disposition

**Confirmed P1 correctness failure:** a losing candidate matched two kept
clips, so placement retired one kept clip onto the other even though those
two clips did not match. The retired clip represented the target second goal;
the remaining keeper represented the first. This is not merely an inferior
quality preference or a compilation opening on the wrong goal.

Track the bounded correctness fix as
[FF-092](../../todo.md#ff-092--losing-bridge-retires-a-kept-clip-that-the-winner-does-not-match),
separate from [FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order)
selection-policy research. Adjacent-event admission is also an
[FF-003](../../todo.md#ff-003--candidate-can-pass-without-exact-event-semantic-evidence)
boundary. The investigation changed no production data, workflow, share or
media and downloaded no video bytes. The subsequent local correction is
recorded in the [FF-092 decision](../../decisions/2026-09-11-losing-candidates-preserve-existing-keepers.md);
deployment and production repair remain separate.

## Event and asset identity

Fixture `1550126`, Venezia–Fiorentina, reported 2–4 at inspection. Mastantuono
has recorded goals at 29′, 30′, and 84′; this report addresses the **30′ goal**.

- First goal: `d0a46df7-e3fc-4892-b748-ee584124f5de`, expected clock center 28.
- Second goal: `d7db1660-1055-4651-b3ef-75d4e6dc3ad3`, expected center 29.
- Workflow: `event-d7db1660-1055-4651-b3ef-75d4e6dc3ad3`.

The following nodes all belong to the second event's comparison scope:

| Label | Asset | MD5 | Duration | Source meaning |
|---|---|---|---:|---|
| A | `86fe5bb9-935f-5cbd-8bba-f7cdf873b6bf` | `731621c93d168bdfac320d60a77482c6` | 18.112 s | First goal |
| B | `3da095d5-23eb-5c83-ac6f-90b9037e17ad` | `6e1003b5a45f3c44f012f29b2eecd213` | 12.757 s | Second goal |
| C | `f46097f2-e624-5fb9-98fa-5a9a02b9b958` | `bf1f217e900aba00b75d0575b2235ad4` | 11.865 s | Both goals / bridge |

A's source, `argy_fut/status/2098491102789767598`, explicitly says first goal.
B's source, `GoalsXtra/status/2098491069528785033`, describes a second strike
and Venezia 1–2 Fiorentina. C's source,
`KalshiFC/status/2098492383193120829`, describes a brace in under two minutes.
These are retained tweet observations, not fresh web fetches. The user
visually identified the remaining video as the prior goal; stored clocks and
the direct hash triangle independently corroborate the distinct actions.

## Recorded chronology

All times are September 11 UTC, from Temporal placement commands and SQL:

1. 19:18:18: a 6.165-second first-goal copy entered the second-goal event.
2. 19:18:42: A replaced that shorter first-goal copy.
3. 19:18:49: B promoted with no losers. Both A and B were kept separately.
4. 19:23:16: C completed vision and matched both. Placement selected existing
   A (`NewWinner=false`) and supplied B in `LoserAssetIDs`; C became a hidden
   accepted variant attributed to A.
5. Five later discoveries of B's exact bytes were credited to A. Nine
   observations of B in total now credit A, while retaining B as their
   immutable `observed_asset_id`.

The selected share A is `s_a1029d0a2013`, clock-verified, popularity 36. B's
`s_1b640c847760` is superseded, not reclaimed. A separate unverified singleton
remains active but is omitted by the normal FF-078 public visibility rule.
That pruning is not what removed B; the supersession transaction did.

## Why the first goal entered the second event

Both searches use `(mastantuono OR Fiorentina) filter:videos`. For API 30′,
the validator expects completed minute 29 with tolerance ±1, accepting 28–30.
For API 29′, it accepts 27–29. The permitted clock windows overlap.

The actual second-event validation results include:

| Clip | Model clock observations | Matched minute |
|---|---|---:|
| A | 27:59, 28:03, 28:08 | 28 |
| B | 29:36, 29:39, 29:42 | 29 |
| C | 28:04, 29:39, 29:42 | 28 |

All passed the soccer/screen gates. `Evaluate` returns the first qualifying
clock, so C's summary says 28 even though later samples directly support 29.
The arithmetic is operating as specified; a readable clock within tolerance
does not uniquely identify adjacent goals. Do not remove tolerance globally
without preserving the existing buildup and clock-normalization regressions.

## Exact matcher reproduction

The offline Go probe decoded the stored big-endian uint64 hashes and called
the actual `Match`, `BestAlignment`, and `IsUpgrade` functions. Input sizes
were A=180, B=127, C=118 samples with the same explicit v2 100-ms contract.
Results are the same in both input orders:

| Direct pair | Hamming 12, ≥30 samples, ≤3 misses | Hamming 16, ≥50 samples, ≤5 misses |
|---|---|---|
| A ↔ B | No: strongest span 4 / 3 misses | No: 17 / 5 |
| C ↔ A | No: 26 / 3 | **Yes: 50 / 5** |
| C ↔ B | **Yes: 63 / 3** | **Yes: 76 / 5** |

C ↔ A's sustained span maps C `[0,50)` to A `[69,119)`, about the first five
seconds of C. C ↔ B's sustained span maps C `[42,118)` to B `[50,126)`, about
the later 7.6 seconds of C. Approximate hash alignment and tolerated misses
are not exact edit boundaries. The important topology is established without
inferring a direct A ↔ B relationship.

A's 18.112 seconds beat B's 12.757 and C's 11.865 on the current duration
tier. The comparator therefore selects A, despite C's higher bitrate.
`dedupAndCommit` then retires every other asset matched by **C**, without
requiring that selected **A** match those assets. This creates B → A even
though the matcher explicitly distinguishes B and A.

The compatibility path has the same assumption. Existing comments saying
every bridged asset must consolidate describe actual code, not just stale
wording. New behavior needs an explicit invariant and Temporal compatibility
coverage; this is not resolved by tuning a hash threshold or changing a name.

## Why the remaining clip also looks lower quality

The first event later selected `b963ebb8-3917-5112-adee-7119ad7023ac`, a
63.854-second CBS source reported as 1920×1080 at about 59.94 fps. The second
event's A remains 1280×720 at 30 fps. That agrees with the reported difference
in encoded specifications, without claiming those fields prove perceived
quality. The CBS source was present in the first event's candidates and absent
from the second event's candidate records at inspection; this audit does not
establish why that discovery differed. Selection is event-scoped, so the first
event's upgrade does not automatically replace A in the second event.

The second workflow completed all 15 usable searches with four unavailable
probes and 124 candidates. Sixty-two candidates failed download, but B was
successfully found, validated and promoted before this removal. Those failures
cannot explain this particular loss of public coverage.

## Bounded correction and remaining limits

The minimum safety requirement is that a kept clip cannot be retired solely
through another candidate's match relationships. A losing bridge must not
cause unmatched existing keepers to collapse into its chosen incumbent.
The local FF-092 implementation leaves all other keepers untouched when the
candidate loses, without a learned quality model or full graph selector.

Direct shared-footage evidence is **necessary, not sufficient**, for a good
replacement. FF-081 still owns coverage, quality and whether a winning bridge
is an acceptable substitute for each removed clip. The bounded correction
must not silently adopt transitive content identity, vote multiplication, or
new global event ownership.

Regression coverage retains this exact triangle, both threshold routes,
the recorded arrival sequence and its permutations, exact-followers after
placement/recovery, category scoping, and atomic popularity/update effects.
Repairing B's production share, root and credit is a separate approved action;
simply clearing `superseded_by` would not restore all placement invariants.

The [checked-in regression evidence](../../../internal/workflow/testdata/README.md)
now retains the exact CSV. Workflow coverage demonstrates the former removal,
the corrected losing path, exact recurrence, and old-history compatibility.
It also preserves winning-path counterexamples: B/C/A and C/B/A still end with
one keeper under the unchanged policy. They belong to FF-081, not to a claim
that this fix provides order-independent coverage. Both saved incident
histories passed offline Temporal SDK replay after the correction.
Full `make check` passed, including the real-Postgres independent-credit and
recovery test. Targeted race checks and 20 repeated workflow regression runs
also passed. The implementation remains local; no production repair or
deployment was performed.

Saved local evidence is under ignored `scratch-audit-2026-09-11/mastantuono/`:
both goal histories, extracted vision/placement results, the three-row
`assets.csv`, and `probe.go` / `match-results.ndjson`. The network-disabled
Go probe completed with `PROBE_COMPLETE` and exit zero. No new human quality
label was inferred from metadata, and no production media export was used.
