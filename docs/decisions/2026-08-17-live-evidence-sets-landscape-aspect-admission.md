# Live evidence sets landscape aspect admission to 1.73–1.82

## Context

The metadata gate admitted aspect ratios from 1.75 through 1.82. Its lower
edge came from the Python-era corpus: most broadcast clips clustered near
16:9, while known letterboxed and social layouts occupied roughly 1.60–1.72.

Elche's 76′ goal on 2026-08-17 produced four candidates rejected before
download at aspect ratio 1.739. Manual review confirmed legitimate goal
footage in at least three. The old boundary therefore caused demonstrated
recall loss before dHash or vision could evaluate the content.

## Decision

Set `HARDFILTER_MIN_ASPECT`'s default to 1.73. Keep the maximum at 1.82 and
keep the gate inclusive at both boundaries.

The value is an empirical separator, not a standard display ratio. It gives
the observed 1.739 cluster room for small encoder variation while remaining
above the prior 1.60–1.72 unwanted band. Tests pin 1.739 and 1.730 as accepted,
1.729 as rejected, and 16:10 as rejected.

## Consequences

The change widens admission only. It does not alter dHash generation, the
per-frame Hamming threshold, the 30-frame window, or the three-miss allowance.
Admitted clips still pass duration, frame-rate, short-edge, vision, and
event-scoped perceptual-dedup gates.

This supersedes only the 1.75 lower edge in the
[2026-07-27 aspect-band decision](./archive-through-2026-08-16.md#2026-07-27--v-phase-rung-3a-hard-filter--the-aspect-band-175182-hard-gate).
The earlier evidence and rationale remain historical truth.
