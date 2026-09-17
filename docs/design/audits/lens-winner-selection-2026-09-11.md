# Lens stoppage-time winner selection — 2026-09-11

## Scope and disposition

Read-only investigation of the user's report that Slavia Praha–Lens's 90+3′
winner shows footage of the preceding goal. The match was September 10 UTC;
the investigation was September 11. Production was not changed or replayed.

The recorded placement history confirms that an 83.925-second CBS video
replaced the earlier 32.981-second winner cut. Its source describes both Lens
stoppage-time goals. Stored hashes reproduce a substantial overlap with the
earlier clip, and the existing duration-led comparator reproduces the choice.
This supports the same compilation-versus-focused-cut defect as the
[Demirović incident](./video-picture-quality-2026-09-09.md#separate-compilation-boundary-demirovic),
under [FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order)
and [FF-003](../../todo.md#ff-003--candidate-can-pass-without-exact-event-semantic-evidence).

**Visual limit:** The user reports the earlier goal in the displayed video.
The source text, hashes, and retained clock output strongly support a
multi-goal compilation, but exact scene boundaries and inclusion of the
winning action have not been independently inspected in this investigation.
The safety reviewer rejected downloading the two exact media files without
explicit export approval; approval was requested. No alternate download path,
new model call, source rewrite, or production repair was attempted.

## Exact identity and recorded timeline

- Fixture: `1635729`, Slavia Praha 2–3 Lens, terminal status `ft`.
- Prior Lens goal: Florian Thauvin, 90+1′,
  event `17dcca29-c0a4-4d1f-960b-b9f65da446d3`.
- Winner: Ruben Aguilar, 90+3′,
  event `431df453-6087-4c43-9283-37c0faa4a951`.
- Workflow: `event-431df453-6087-4c43-9283-37c0faa4a951`.
- Winner first seen: 20:54:00.267694 UTC; workflow started 20:55:00.530903 UTC.

All placement times below are September 10 UTC, from retained Temporal commands.

| Time | Newly selected asset | Duration | Replaced |
|---|---|---:|---|
| 20:57:48.380 | `50c7078d-7ab5-5e17-967c-6bd47f4520e2` | 11.562 s | None |
| 20:57:56.000 | `5353c009-ede7-5d0a-933b-e566b40fa019` | 32.981 s | The 11.562 s cut |
| 21:12:37.166 | `4a734cdf-821e-5ccc-bf42-6487b4f399c2` | 83.925 s | The 32.981 s cut |

The current active share is `s_0eab822aa7ea`, popularity 19, clock-verified at
92′. The earlier shares `s_c8ebd530b5e2` and `s_901bc16e9848` are superseded,
not reclaimed: all three asset reclamation markers were null at inspection.
Their historical rows remain. The single listed video is a placement result,
not merely singleton filtering hiding an otherwise active focused cut.

The final source is [CBS Sports Golazo's two-goal post](https://x.com/CBSSportsGolazo/status/2098156592134869116),
found on search attempt 8 at 21:12:17.562535 UTC. Its stored text describes two
stoppage-time goals, not just Aguilar's action. The earlier 33-second primary
source is [Ayuba Football's Aguilar post](https://x.com/ayubafootball/status/2098153765652049945).
These source identities come from stored candidate observations; their public
pages were not fetched during this investigation.

## Why clock validation passed

The workflow input correctly retained `minute=90`, `extra=3`; normalization
therefore expects completed minute 92. The vision activity samples 25%, 50%,
and 75% of each clip's duration.

For the final CBS video, the recorded model output was:

| Sample position | Visible clock reported by the model |
|---|---|
| Approximately 20.98 s | None |
| Approximately 41.96 s | Main clock `90:00`, allowance `+5`, elapsed stoppage clock `02:44` |
| Approximately 62.94 s | None |

All three samples voted soccer and none voted screen recording. The middle
reading normalizes to 92:44; the numeric check uses minute 92 and returns
`clock matches minute+period`. The frozen-boundary/unknown-stoppage-minute
fallback did **not** cause this acceptance. Earlier cuts also retained exact
90:00 plus 2:xx readings.

The current validator verifies a usable clock in the clip, not that the
opening action is this event, that every scene belongs to it, or that the
whole clip is a better presentation. The model is not asked to classify the
source text as a compilation. A tighter minute tolerance would not reject
this recorded minute-92 match.

## Deduplication and keeper reproduction

The offline probe imported the actual Go `Match`, `BestAlignment`, and
`IsUpgrade` functions and decoded the exact exported hash bytes. No new
threshold, crop, model, or inferred re-encoding was used. The export retained
the three fixed asset IDs only, including their version, metadata and hashes.

For CBS (839 samples) versus the preceding keeper (329 samples):

- Primary: 120 aligned samples, three misses, Hamming allowance 12; passes
  the 30-sample requirement. Mean Hamming over the span: 5.425.
- Sustained: 148 aligned samples, five misses, Hamming allowance 16; passes
  the 50-sample requirement. Mean Hamming over the span: 5.919.
- The sustained span aligns CBS samples `[222,370)` with prior samples
  `[181,329)`, approximately 22.2–37.0 s versus 18.1–32.9 s.
- Individual tolerated misses reach Hamming 28 in that sustained span; they
  are misses, not evidence that the configured allowance was increased.

This is a strong shared-sequence match, not just one coincident frame. It
does not prove that the rest of CBS's video is suitable replacement content.

The comparator caps the completeness benefit at 60 seconds, then compares
capped duration before other metadata. Here that is 60 versus 32.981 seconds,
well beyond its 15% band, so `IsUpgrade` returns true on duration alone.
Reported FPS does not enter this comparator. The final file's higher bitrate
is not required for the decision. The earlier 33-over-11-second replacement
also reproduces with both hash routes and the current quality comparator.

## Search and remaining action

The workflow completed at 21:20:16.682867 UTC with 15 usable attempts, eight
unavailable probes, 74 candidates and one kept asset. Twenty candidates had
download failures. Those are separate discovery limitations; they do not
explain why an already-selected focused clip was superseded.

The immediate missing check is approved visual inspection of the exact CBS
and 33-second files. Production restoration, suppressing a share, changing a
root, reassigning credit, replaying the workflow, or deploying a policy fix
would each require the relevant explicit approval. Do not assume the rejected
media-export action authorizes any such mutation.

For FF-081, preserve this as a content-selection boundary alongside Demirović:
more total runtime can include other events rather than useful context for
the target event. Crop normalization and a better picture-quality score do
not resolve that distinction by themselves. This investigation changes no
accepted keeper labels or production selection rules.

Local evidence is under ignored `scratch-audit-2026-09-11/lens-winner/`:
`history.json`, extracted `vision-results.json` and `placements.ndjson`,
`assets.csv`, and `probe.go` with `match-results.ndjson`. The network-disabled
Go probe finished with `PROBE_COMPLETE` and exit zero. Media files are not
part of that evidence at this checkpoint.
