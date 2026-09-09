# FF-081 natural cadence-pair review — 2026-09-08

## Scope and disposition

This is a bounded offline review of the five pairs preserved by the
[September production audit](./pre-rollout-evidence-2026-09-08.md#ff-081082083-quality-evidence-now-supports-the-next-review).
It does not change production selection, dHash matching, vision, or visibility.
Active work remains [FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order).

The sample contains five approximately-60-fps variants that lost their first
comparison and their five 30-fps direct keepers. All ten local MP4 copies
passed the previously recorded SHA-256 checks. No live production query was
needed. Here, **keeper** means the captured direct successor, not a claim
about today's public rank or latest root.

The initial review used decoded source-frame sheets, consecutive frames, stream
metadata, presentation timestamps, and frame-difference diagnostics. It did
not include real-time audiovisual playback or a new user quality judgment.
Those initial conclusions were assistant observations, not accepted human
labels. The later [Adams acceptance and cadence experiment](#adams-acceptance-and-cadence-experiment)
below record the subsequent explicit user judgment and controlled measurements.

## Findings

1. All five keepers have a large `talkFootX` watermark and a tilted/framed
   presentation absent from the corresponding losing copy. The other copies
   still have ordinary broadcast branding; some are strongly saturated.
   Cleaner framing is observable, but an overall quality winner remains a
   product judgment.
2. Four keepers win on duration before the comparator considers compression
   density or resolution. Palacios survives a tie inside both metadata bands.
   No case requires an implementation malfunction to explain the result.
3. Every keeper has more exact-MD5 observations than its losing variant.
   Selecting the most frequently seen encoding would preserve all five
   choices. Repost frequency does not measure presentation quality.
4. Encoded FPS is not proven source-motion cadence. El Khannouss has a strong
   alternating near-repeat pattern despite its approximately-60-fps stream.
5. Longer is not necessarily a superset. Palacios's cuts overlap but preserve
   different portions of the play. Mariano's longer cut adds substantial
   buildup; that is a real completeness-versus-presentation tradeoff.

These pairs were selected for a cadence disagreement, one per event, with
bounded download size. All five keepers share a visible editing family. They
are not five independent demonstrations that most 30-fps keepers are worse,
nor a representative sample of all 79 lower-reported-FPS keeper edges.

## Why each keeper survived

Durations are the persisted container-derived values. Density is the current
`bitrate / (width * height)` proxy. All pairs are 1280×720 except Mariano's
60-fps copy, which is 1280×704. Exact observations are immutable variant
attributions, not the keeper's aggregate dHash popularity.

| Event | 60-fps / 30-fps duration | Current reason | 60-fps density versus keeper | Exact observations, 60 / 30 |
|---|---|---|---|---|
| El Khannouss 39′, Stuttgart–Köln | 7.337 / 10.730 s | Duration gap 31.6% | +1.1% | 2 / 13 |
| Che Adams 82′, Fiorentina–Torino | 7.552 / 9.216 s | Duration gap 18.1% | +23.7% | 2 / 4 |
| Palacios 42′, Fulham–Palace | 9.365 / 10.880 s | Duration gap 13.9%; density within 10%; same pixels: incumbent tie | −5.1% | 11 / 19 |
| Mitchell 35′, Fulham–Palace | 8.256 / 12.352 s | Duration gap 33.2% | +11.0% | 2 / 14 |
| Mariano 36′, Alavés–Osasuna | 5.479 / 15.146 s | Duration gap 63.8% | +27.1% | 1 / 10 |

The [current comparator](../../../internal/domain/video/quality.go) caps
duration at 60 seconds and uses `abs(a-b)/max(a,b) > 0.15`. Outside that band,
longer wins without consulting other inputs. Inside it, a 10% spatial-density
advantage wins, then pixel area breaks a tie. FPS and exact popularity are not
inputs.

### Content and presentation observations

- **El Khannouss:** Both show the finish and initial celebration. The keeper
  adds earlier buildup and later celebration. The losing copy has cleaner
  rectangular framing, but its encoded FPS does not establish a cadence win.
- **Che Adams:** The shorter copy has cleaner framing and shows the finish
  and celebration. The keeper adds roughly 1.5 seconds before the aligned
  action. This is the strongest candidate for reviewing a cleaner,
  higher-cadence copy as the sole replacement, rather than merely exposing
  another variant.
- **Palacios:** The shorter copy starts around broadcast 41:41; the keeper
  starts around 41:46. The former preserves earlier buildup, the latter more
  celebration. Their best stable alignment covers only 54.8% and 47.2% of
  their respective hash timelines. A duration advantage does not establish
  coverage of the missing footage.
- **Mitchell:** The keeper adds later celebration, including the knee slide.
  The losing copy has cleaner framing and more distinct temporal changes in
  the decoded frames. Whether celebration justifies retaining both is an
  unresolved editorial choice, not a question answered by bitrate.
- **Mariano:** The short copy begins near the final pass/finish. The keeper
  adds about nine seconds of earlier buildup. Automatically favoring 60 fps
  here would discard meaningful footage even if the short presentation is
  cleaner.

Tilt and framing alone do not prove that a phone filmed a screen. They can
also come from digital edits. Do not turn these observations into confirmed
FF-052 screen-detection failures. Current vision does not provide a general
watermark, crop, or presentation-quality score.

## Reported versus motion cadence

All files decoded successfully. Their frame counts and stream durations
support the stored approximately-60/30-fps encode rates. That checks the
metadata plumbing; it does not prove unique source frames.

For the 60-fps copies, native-resolution `mpdecimate` retained:

| Event | Decoded frames | Default threshold kept | Stricter near-repeat threshold kept |
|---|---:|---:|---:|
| El Khannouss | 437 | 325 | 436 |
| Che Adams | 447 | 438 | 446 |
| Palacios | 556 | 470 | 552 |
| Mitchell | 490 | 438 | 486 |
| Mariano | 326 | 326 | 326 |

Default settings were `hi=768:lo=320:frac=0.33`; the stricter repeat test used
`hi=64:lo=32:frac=0.33`. These counts are **not measured native FPS**. Motion,
compression noise, overlays, and scene cuts affect the filter. The 30-fps
copies retained all frames under the strict test; Adams retained 265/276
under the default test, while the other four retained all frames.

At 320×180 grayscale, El Khannouss's first two seconds alternate median
adjacent-frame differences of about 5.46 and 0.12 on a 0–255 scale. Consecutive
source frames also show the near-repeat pattern. Its default-decimation count
therefore cannot justify calling it a native 45-fps source either. The other
four copies have substantially more distinct temporal changes and lack that
consistent alternating pattern in the inspected interval. Original masters
would still be needed to distinguish native capture from interpolation.

One additional input limitation is now explicit:
[ffprobe parsing](../../../internal/infra/ffmpeg/client.go) prefers the
container bitrate over the video stream bitrate. In these ten files the
container value exceeds video-stream bitrate by approximately 114–135 kb/s.
Audio and container accounting therefore enter the spatial-density proxy.
These values do not imply a pure visual-quality measurement; stream bitrate
also remains an encoding proxy, not proof of source detail. Preserve the
distinction when designing replacement evidence rather than silently changing
the meaning of historical `bitrate` rows.

## Offline policy replay

The existing cadence-aware experiment uses stable-offset coverage plus
independent pixel-area, reported-FPS, and spatial-density floors. It does not
score visible presentation defects or estimate native motion cadence.

| Event | Experimental coverage | Cadence-aware action |
|---|---|---|
| El Khannouss | Keeper contains shorter timeline | Keep both: reported-FPS floor blocks the keeper; shorter lacks coverage |
| Che Adams | Both covered at the experimental 80% boundary | Keep only the 60-fps copy |
| Palacios | Partial overlap | Keep both |
| Mitchell | Keeper contains shorter timeline | Keep both |
| Mariano | Keeper contains shorter timeline | Keep both |

The older per-frame-density experiment keeps both in all five cases. The
cadence-aware version resolves Adams but still exposes four pairs. Its
El Khannouss decision also shows how an inflated FPS label can prevent a
collapse. This is evidence against deploying the experiment unchanged.

The graph does not solve these policy choices. A direct-cover solver can
ensure that each hidden variant has a selected direct substitute and remove
arrival-order dependence. It cannot decide that a watermark is worse, that
added buildup matters more than celebration, or that a 60-fps label contains
repeated motion. A connected dHash component remains evidence topology, not
a transitive duplicate identity.

## Durable evidence and reproduction

The [natural cadence corpus](../../../scripts/audit_video_quality/testdata/cadence-pairs-2026-09-08.json)
preserves identities, source-copy SHA-256, complete derived dHash sequences,
persisted metadata, exact observations, decoded-frame diagnostics, and
current/experimental outcome snapshots. It contains no media, tweet text, or
media URLs. Its status is now `partially_reviewed`: Adams has an explicit
`human` label; the other four pairs remain unlabelled. The previously accepted
August corpus remains separate and unchanged.

[Regression tests](../../../scripts/audit_video_quality/cadence_pairs_test.go)
recompute both match routes, current quality preference, stable alignment, and
experimental actions. Passing proves reproducibility, not product acceptance.
Run the package through the pinned Go test container described in
[testing](../../testing.md).

Local, ignored evidence is under `scratch-audit-2026-09-08/`: `media/`,
`media-sha256.txt`, `pair-review/measurements.json`, frame sheets, and
`review-pairs.mjs`. Reproduction used ffmpeg from the locally retained worker
image `3723ce2aa6476a2c85e2bb24351336eda890683d`, invoked only as a disposable
offline tool with no production mounts or credentials. Source-frame sheets
sample eight frames over each video stream; the additional El Khannouss sheet
shows consecutive source frames 60–71. No media was added to Git.

## Recommended next decision

Adams is now accepted as the cleaner sole keeper. Review Palacios next as the
different-footage case and Mariano as the meaningful-buildup tradeoff.
Record two independent user judgments: **may one replace the other?** and
**which presentation is preferable?** El Khannouss is the guard against using
reported FPS as decisive evidence.

The later [2026-09-09 picture-quality pilot](./video-picture-quality-2026-09-09.md#new-user-observations)
records tentative user preferences for Palacios review A and Mariano review B.
These are separate presentation notes, not final replacement labels; the
original corpus and experimental snapshots remain unchanged.

The next policy should distinguish coverage, presentation defects, and
technical quality. Exact-MD5 frequency can remain corroborating evidence;
this sample rejects using it as the primary presentation selector. Do not
implement new vision scoring, an FPS bonus, new metadata columns, or graph
visibility from this review alone. Test an agreed replacement policy against
both accepted labels and these preserved boundary cases first.

## Adams acceptance and cadence experiment

The user reviewed the downloaded Adams files on a laptop and explicitly chose
the shorter right copy (`1d6e5caa-f284-5069-8db3-b6378d090621`): it looks better
and retains essentially all useful content. The pair should collapse; the
extra 1.7 seconds do not justify retaining the watermarked keeper. Watermark
clutter is a lower-priority preference, not a newly approved vision gate.
This judgment does not isolate FPS as its sole cause and does not approve a
new duration percentage. The natural-corpus regression now pins that label
separately from the unchanged current left-keeper outcome.

The new [offline cadence command](../../../scripts/audit_video_cadence/README.md)
tests repeat patterns in native-cadence decoded frames. It adds no activity,
model call, stored asset field, or production selector. The command and its
generated controls run in network-isolated, memory-capped containers. The
measurement contract, fixed experimental parameters, resource limits, and
reproduction commands live in its README.

### Generated controls

All ten real-ffmpeg controls passed. The initial nine cover native-rate motion
generated at 30/60 fps; 30/20/15→60 duplicated motion; a lossy second encoding of repeated
30→60; blended 30→60 interpolation; static footage; and a moving edge overlay
on static footage. The duplicated controls produced factors two, three, and
four. Static scenes stayed inconclusive. Native-rate and blended controls both
returned `no_repeat_pattern_detected`, deliberately exposing why that result
cannot certify original capture FPS.

A tenth control adds a central animation over known duplicated background
motion. It did mask repeat evidence. This is a known blind spot,
not a reason to mark the source as native-rate. Pure tests also pin mixed
windows, repeat-phase shifts, factors 2–6, low motion, cut-only footage,
timestamp faults, and incomplete decode rejection.

### Preserved natural files

All ten source-copy SHA-256 identities remain attached to the evidence. The
first measured run used `repeat-pattern-v1` and the retained `3723ce2` ffmpeg
image. Complete output is in the ignored local `cadence-experiment.json`.
Here, **no pattern** means no supported repeated-frame pattern was detected;
it is not a measured native-frame-rate claim.

| Event / encoded FPS | Repeat windows | No-pattern windows | Inconclusive windows |
|---|---:|---:|---|
| El Khannouss / ~60 | 3, all factor two | 1 | 0 |
| El Khannouss / 30 | 0 | 5 | 1 short tail |
| Adams / 60 | 0 | 4 | 0 |
| Adams / 30 | 0 | 4 | 1 short tail |
| Palacios / 60 | 0 | 4 | 1 timing fault |
| Palacios / 30 | 0 | 5 | 1 timing fault |
| Mitchell / 60 | 0 | 4 | 1 short tail |
| Mitchell / 30 | 0 | 6 | 1 short tail |
| Mariano / 60 | 0 | 3 | 0 |
| Mariano / 30 | 0 | 7 | 1 short tail |

El Khannouss's positive windows cover approximately 0–2, 2–4, and 6–7.27
seconds, with cycle agreement 96.7%, 83.3%, and 97.4%. Do not average the mixed
result into an asserted 30-fps source. The two Palacios timing windows include
the duplicate/gapped PTS already seen in the earlier probe; the experiment
leaves them inconclusive rather than treating them as repeat evidence.

Observed elapsed time was 817–1320 ms per natural file, including ffprobe,
decode, scalar analysis, and checksum, in a two-CPU/2-GiB container. This is a
single local run over short warm-cache files, not a load test or accepted
production overhead. The current command separately probes and decodes; any
future pipeline integration needs its own latency measurement and should
consider the existing decode pass before adding another one.

### Disposition

This pass supplies an accepted keeper judgment plus evidence that an encoded
FPS label can exaggerate motion cadence. It does not yet supply a reliable
source-FPS estimator or justify a production FPS bonus. Keep watermark scoring
deferred, finish the Palacios/Mariano content judgments, and test any proposed
replacement relation against the accepted corpus and these failure cases.
The quality-code comments now describe spatial bitrate density as a proxy;
they no longer claim that it detects upscaling or establishes native detail.

Validation: `make check-short` passed with the pinned, capped offline tool
containers; the separately required real-ffmpeg control suite passed; all ten
source-copy checksums match the preserved corpus; and local documentation
links plus `git diff --check` passed. No production state or selection policy
changed.

## Palacios and Mariano: content tradeoffs

This follow-up reviews the same preserved files and current selection/read
code. It adds no live production query or user label. The content descriptions
come from the sampled source-frame sheets, not real-time playback. Both pairs
remain unlabelled until the user reviews their cuts.

| Pair | Shorter presentation | Longer presentation | Unsettled choice |
|---|---|---|---|
| Palacios 42′ | 9.365 s, encoded 60 fps; earlier buildup from about 41:41 through the finish; cleaner rectangular framing | 10.880 s, encoded 30 fps; starts about 41:46, retains later celebration; tilted framing and large watermark | Does the later celebration warrant a second clip, or may the cleaner cut replace it? |
| Mariano 36′ | 5.479 s, encoded 60 fps; final pass, finish, and reaction; cleaner framing | 15.146 s, encoded 30 fps; about nine seconds of additional buildup before the shared finish; tilted framing and large watermark | Is the extra buildup valuable enough to retain both cuts, or should one be the sole presentation? |

The assistant's provisional recommendation is to prefer the cleaner Palacios
cut alone if playback confirms that its ending is sufficient, and retain both
Mariano cuts if their quality difference is meaningful in playback. Neither
recommendation is an accepted label. This refines the earlier experimental
`keep_both` result: different footage alone does not establish that two public
clips are useful. Celebration is not categorically worthless, and buildup is
not categorically mandatory. These examples should test that distinction
before choosing a general replacement rule.

The user has accepted Adams as a sole shorter keeper. No global duration
boundary, FPS bonus, watermark detector, or replacement policy has been
accepted. In particular, changing 15% to 20% may fix Adams's metadata outcome
without explaining either of these content choices. The cadence probe's lack
of repeat evidence does not certify native 60-fps motion.

### Selection must agree with the public read model

The [current placement path](../../../internal/workflow/event_pipeline_placement.go)
selects a keeper with `IsUpgrade`, consolidates matched losers, and moves their
popularity credit. The [public read query](../../../internal/infra/pg/video_repo.go)
then separately filters and ranks active shares. A replacement policy must
settle all of these boundaries together:

- **Visibility:** Under [FF-078](../../decisions/2026-08-30-popularity-prunes-public-singletons.md),
  a verified clip at popularity three or greater hides all popularity-one
  clips. Mariano's clean variant has one exact observation in this capture;
  the longer variant has ten, and its captured root has aggregate popularity
  fifteen. If a keep-both policy leaves the cleaner root with one credit and
  the longer root above the threshold, the API will still hide the cleaner
  root. Exact counts do not predict the complete reselected event's root
  totals. Decide whether an intentional complementary cut remains subject to
  singleton pruning; do not claim that retaining two roots guarantees two
  displayed clips.
- **Ordering:** Within timestamp categories, public rank is popularity, file
  size, age, then share ID. Selecting a technically preferable alternative
  does not put it first. Decide which result should lead separately from
  which results may remain available.
- **Popularity:** One source observation must retain one popularity credit.
  Two selected outputs must not each inherit the same old aggregate count.
  Stable attribution across multi-matches needs an explicit rule, using the
  immutable observed variant rather than copying root totals. Raw exact-MD5
  frequency is evidence about distribution, not the presentation-quality
  winner.
- **Coverage:** A connected dHash component is not a promise that every pair
  is substitutable. Test direct replacement and bridge cases; do not collapse
  an entire connected component merely to obtain one public representative.
- **History and delivery:** Accepted losing bytes are already retained under
  [FF-083](../../decisions/2026-08-31-accepted-variants-form-direct-lineage.md)
  until normal media reclamation. Two public presentations need not require
  a second copy of those bytes, but can increase playback traffic. Preserve
  existing share-link resolution and retention; historical reselection is a
  separate migration/replay decision, not part of this offline review.

Returning ordinary additional shares can use the existing API and
`event.update` contract. Labels such as “full buildup” or “cleaner cut” would
need a separately agreed backend/frontend contract. No schema addition,
frontend change, new inference call, or deployment is justified by these two
unaccepted judgments alone.

The local ignored `content-tradeoffs-review.zip` contains the four original
MP4s and a static side-by-side `review.html` for laptop playback. It requires no
server or network connection after download. The encoded-FPS labels are not
native-cadence claims. Preserve accepted user judgments in the natural corpus
after review, then test a coherent selection/visibility policy offline before
changing production.

The subsequent [aligned-section experiment](./video-overlap-review-2026-09-08.md)
now locates shared footage and unsupported edges/gaps while retaining these
quality predictions and accepted labels. It confirms the Palacios/Mariano
content distinction but also shows why raw support percentages cannot replace
the Adams and Mbappé judgments. It changes no keeper or visibility policy.
