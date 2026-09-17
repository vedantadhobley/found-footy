# Clock-field failure frequency — 2026-09-14

## Result

FF-094 recurs in retained production observations; it is not limited to the
September 13 offline Maitland-Niles revalidation. There are two faulty paths:
adding a secondary timer to an already running main clock, and discarding the
main clock's minute precision merely because an announced allowance is present.

The read-only snapshot at **05:49:30 UTC** contains 4,672 rejected candidate rows
with frame observations discovered since August 31, plus 78 retained accepted
evaluations. Four accepted evaluations came from the approved restoration;
exclude those from natural-production counts. The other 74 span the new
accepted-evidence capture period, not the full rejected-row window.

| Measurement | Result | Meaning |
|---|---:|---|
| Rejected rows with a non-frozen main timer and positive secondary minutes | 50 across 18 events | Exposed to double-counting; not necessarily an incorrect final rejection |
| Rejected rows whose outcome changes in the secondary-field-removal control | 20 across 9 events | 17 become unverified; 3 become verified under existing fallback rules |
| Distinct event/frame arrays among those 20 | 14 | Repeated candidate evidence must not be called separate lost videos |
| Natural accepted evaluations changing to rejected in the combined control | 3 of 74, all Adžić | Two used added secondary minutes; one bypassed minute checks through the announced allowance |

Neither denominator measures a population false-positive/negative rate. Older
accepted observations expired before FF-093; candidate followers share evidence;
most original footage has not been visually reviewed in this pass. The 20
changed rejects are review leads, not authorization to accept or replay them.

## Concrete cases

Four Maitland-Niles rejected candidate rows from September 6 retain precisely
`95:04, 95:15, 95:27` plus secondary `05:00`, normalized to minute 100 against
expected minute 95. Removing only the secondary fields makes all four
unverified. This establishes the historical faulty path, independently of the
[fresh revalidation](./maitland-niles-selection-preview-2026-09-13.md).
It does not establish the expired original evaluation of every accepted variant.

The changed rejected rows group as follows:

| Event | Candidate rows |
|---|---:|
| Maitland-Niles 90+6′ | 4 |
| Samardžić 90+6′ | 2 |
| Lassiter 90+3′ | 1 |
| Wöber 90+13′ | 2 |
| Damiani 45+1′ | 1 |
| Welbeck 90+4′ | 2 |
| Giuliano Simeone 90+5′ | 2 |
| Adžić 90+4′ | 3 |
| Reinildo 90+6′ | 3 |

For Adžić, event `deb550ac-d037-4d30-934e-e35cc0d93429`, fixture `1550124`,
three retained accepted evaluations reveal the inverse problem:

- `7f796464-cdec-4574-a485-fc51d2d20536`: `90:33–90:37` plus `04:00` became 94.
- `23305026-50b6-47e0-a0ba-817a4dcad8be`: `90:28–90:36` plus `+4:00` became 94.
- `1062fcf6-87a2-4c61-9b15-7c075b831616`: `90:42–90:59` plus announced `+4`
  was treated as frozen and verified on period alone.

The stored target is minute 93. Main-clock-only controls reject all three at
the unchanged ±1 tolerance. These are proven evidence/verification defects;
this pass does not establish the clips' semantic content or current public rank.

## Method and limits

An explicit repeatable-read, read-only transaction used a 15-second statement
timeout. The projection contains frame fields, evaluator results and scoped
identities, without credentials, media URLs or new model calls.

The offline harness imports unchanged `vision.Evaluate`. For rejected rows it
uses current event fields only when their normalized expected minute and period
match the retained original expectation. All included baselines reproduce the
stored outcome and reason. It excludes 12 changed expectations and 1,449 rows
without clock expectations; the remaining 3,211 rejects and all 74 natural
accepted evaluations reproduce. No included baseline mismatch occurred.

Three diagnostic modes clear secondary fields, allowance fields, or both,
only beside raw `MM:SS` timers not exactly at a possible 15/30/45/90/105/120
minute boundary with zero seconds. This is a deliberately simple control,
not a shippable parser or a determination that every reported field is visually
wrong. The Wöber control uses the existing leading-digit rebase; that is not new
evidence that its main-clock transcription was correct. No tolerance, soccer,
screen, period or downstream selection policy changed.

## Proposed fix boundary

Preserve raw seconds and running/frozen evidence before reducing clocks to
integer minutes. An announced allowance must neither add elapsed minutes nor
erase a running timer. Only combine a secondary timer with a compatible frozen
period-boundary main clock; never double-count an already cumulative clock.
Retain ambiguous period evidence without inventing visible labels. Keep actual
frozen, compact-stoppage, reset-half, extra-time and no-clock regressions.

Strengthen extraction examples so `+5` cannot legitimately mean `05:00`, but
enforce the arithmetic invariant in code regardless of model compliance.
Replay saved observations before changing the prompt or conducting bounded new
model checks. Whether a truly frozen clock alone deserves verified status is a
separate policy decision, not permission to retain the running-clock bypass.
Global ±1 remains unchanged. No implementation or production repair occurred.

Local artifacts: `scratch-audit-2026-09-14/clock-frequency.sql`,
`clock-evidence.ndjson`, `clock_control.go` and `clock-controls.json`.
The control ran in pinned Go 1.25.11 Docker with network disabled, a 2 GiB cap
and two CPUs. Evidence SHA-256:
`59a078113b8b2e6131d51f85308de9014284c630f874c79fa99e19b26c08ecf6`.
Control report SHA-256:
`48fcfc96669683dfeb5f681690e1ba3467ca664591b0a2f1c664920046c4e3e6`.
