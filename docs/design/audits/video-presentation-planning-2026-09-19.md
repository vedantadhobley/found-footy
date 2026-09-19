# Quality-first video presentation planning — 2026-09-19

This point-in-time audit tests how retained selected roots could produce a
quality-first public presentation. It is an offline experiment for FF-078 and
FF-081. It does not change production ranking, visibility, keeper selection,
or stored support.

## Question

The current read model sorts timestamp-verified clips first and then sorts by
direct support. FF-078 also hides popularity-one roots when another root reaches
the support threshold. That makes recurrence decide the first presentation and
can hide a technically stronger or independently edited clip.

The target is narrower than a universal video-quality score:

- choose the strongest standalone first clip only when the retained evidence
  establishes one;
- preserve later clips that may add footage the first clip does not cover;
- hide a clip only through direct replacement evidence, never graph
  transitivity; and
- keep source recurrence separate from quality.

## Method

The new `-presentation-json` mode reads the same header-addressed FF-081 corpus
as the existing graph experiments. It considers only active, playable,
non-superseded roots whose event remains non-removed in the audit hierarchy.

The planner applies these boundaries:

1. Timestamp-verified roots form the primary tier when present. Unverified
   roots enter primary selection only when no verified root exists.
2. Technical quality is a Pareto relation over pixel area, encoded frame rate,
   and spatial bitrate density. A dominating clip retains at least 90% of each
   known signal and improves at least one by 10%.
3. Capped duration is a separate standalone-context signal. It cannot buy away
   a material technical loss. A quality/context tradeoff remains ambiguous.
4. Direct replacement requires a direct relation under the current dHash
   thresholds plus cadence-aware stable-offset containment. The planner does
   not follow transitive paths.
5. After a unique primary, remaining candidates are emitted in technical
   Pareto layers. Members of one layer have no invented order.
6. Direct support and exact-MD5 observations remain descriptive recurrence
   evidence. Neither selects quality.

This is deliberately conservative. Encoded cadence can overstate distinct
motion. Resolution and bitrate metadata do not detect screen recordings,
watermarks, editorial chrome, crops, or wrong semantic content. A verified
clock establishes time compatibility, not full clip quality. The report marks
unverified footage without a direct visual anchor to the selected set for
review.

The diagnostic dHash relation is recomputed across selected roots even when
their timestamp-verification categories differ. This is useful shared-pixel
evidence, but it never transfers clock verification or rewrites the
category-scoped production placement graph.

## Snapshot results

The read-only September 19 snapshot contained:

| Measure | Result |
|---|---:|
| Events with playable selected roots | 590 |
| Events with multiple roots | 288 |
| Events affected by FF-078 singleton pruning | 233 |
| Hidden roots | 425 |
| Hidden timestamp-verified roots | 80 |
| Hidden unverified roots | 345 |
| Hidden roots without a direct selected substitute | 418 |
| Events whose current primary is technically dominated | 42 |
| Dominated primaries with a unique standalone frontier | 8 |
| Dominated primaries with an unresolved standalone tradeoff | 34 |
| Unverified candidates requiring review | 412 |

The 418 count does not assert that every hidden root adds useful content. It
shows that FF-078's event-global support threshold is not direct redundancy
evidence. A stricter visibility policy needs an explicit substitute edge or a
separate product choice to hide independent presentations.

The eight unique-frontier cases are review candidates, not safe production
changes. Metadata does not detect every presentation defect. The 34 ambiguous
cases prove why one weighted quality score would be misleading: a technically
stronger short cut often competes with a longer contextual clip.

## Raphinha 25′ case

Event `ad17841a-04d4-4cfb-960c-34b0d47e350e` currently exposes one root:

| Role | Asset | Evidence |
|---|---|---|
| Current primary | `cc684047-396b-55af-980a-5342e54327f8` | verified; 1280×720; 60 fps; 32.490 s; direct support 20; exact observations 2 |
| Technical frontier | `d2e6f1dc-5a95-5b43-a4e2-f70afdef2e82` | verified; 1466×828; 60.06 fps; 12.631 s; direct support 1 |
| Manual review | `4ca5b438-0a9a-54fc-bf5e-7c3df36cd54d` | unverified; 1920×1080; 29.87 fps; 55.360 s; no direct edge to the primary |
| Manual review | `e88e58df-afca-5a2b-a43b-10d606064e66` | unverified; 1920×1080; 27.67 fps; 46.058 s; no direct edge to the primary |

The 1466×828 clip technically dominates the current 720p primary, but it is
19.859 seconds shorter. Both remain on the standalone frontier, so the planner
correctly refuses to choose a primary. The two longer 1080p clips have no
visible clock in retained validation evidence and do not directly dHash-match
the verified component. The graph therefore cannot prove their event identity,
their relationship to the current clip, or whether their extra duration fills
the missing sequence and replays. They require content review or stronger
matching evidence.

The current primary's support of 20 is useful recurrence evidence. It is not
evidence that 720p is the highest-quality presentation, nor that the three
hidden roots are redundant.

## Resulting design boundary

The graph can safely answer direct overlap and containment questions. It cannot
infer semantic equivalence through connected components, and it cannot score
screen recordings or editorial quality from container metadata.

The next runtime policy should therefore use three distinct decisions:

1. **Keeper selection inside a direct match:** FF-081 technical and human
   quality policy.
2. **First public presentation across selected roots:** verified reliability,
   then a technical-plus-context Pareto frontier.
3. **Additional public presentations:** keep possible uncovered context;
   suppress only through direct containment and an accepted visibility rule.

Popularity remains source recurrence. It may support a later decision to hide
a weak redundant singleton, but it must not select the best-quality clip or
stand in for a replacement edge.

## Next evidence

Review the eight unique-frontier primary candidates and representative members
of the 34 ambiguous cases. Review the two long Raphinha clips against the full
720p sequence. Use those labels to decide whether the standalone frontier is a
runtime gate, whether effective cadence is required, and what direct-cover
condition may replace FF-078's event-global singleton suppression.
