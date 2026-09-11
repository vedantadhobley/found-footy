# Losing candidates preserve existing keepers

## Context

The [Mastantuono incident](../design/audits/mastantuono-bridge-removal-2026-09-11.md)
exposed an invalid placement decision. Incoming C directly matched kept clips
A and B. C lost to A, but placement also retired B onto A even though A and B
failed both dHash routes. Later exact B observations then credited A through
that committed alias. This was not a hash-threshold failure.

FF-083 retained the evidence graph, not a replacement selection algorithm.
Its direct-decision records cannot make an incorrect workflow decision valid.

## Decision

When an incoming candidate loses to an existing keeper, only that candidate
and its exact-byte followers collapse onto the selected keeper. Other kept
assets retain their shares, popularity, and aliases. The placement has no
`LoserAssetIDs`. It still records the accepted MD5 variant, credits its sources
once, and publishes the existing event-local update after durable placement.

When the incoming candidate wins, the current direct-match replacement policy
remains unchanged. No matcher thresholds, clock admission, quality comparator,
public visibility rule, schema, or frontend contract change in this slice.

`ff-092-preserve-incumbents-on-loss`, version 1, selects this policy at pipeline
initialization. Both atomic placement and the older separate-activity path
honor it. DefaultVersion retains the former commands, including empty-list
payload shape, popularity transfers, and alias redirects. An execution already
past this initialization point keeps its old policy when replayed; a fresh
execution, including failed-run recovery, receives the new policy.

## Verification and boundaries

The checked-in three-asset hash fixture reproduces the exact production
triangle, including C/A matching only the sustained route. Workflow tests cover
all six arrival permutations, both restored-keeper orders, exact followers,
later exact recurrence, activity retry, update publication, and both versioned
command paths. Real-Postgres coverage checks independent credit, active shares,
retry idempotency, recovered aliases and read-derived rank. Offline SDK replay
also exercises the already-exported Mastantuono and Lens incident histories.

This is not an arrival-independent selector. If C arrives earlier and wins,
the unchanged replacement policy can still retire B, followed by A replacing
C. Those permutations remain explicit characterization tests, not claims of
complete content preservation. FF-081 still owns overlap coverage and quality
tradeoffs; FF-003 owns adjacent-event semantic admission. The graph selector
and crop experiments remain offline.

Deployment cannot undo existing relationships. Production repair must restore
the affected root, share, candidate credit, aggregate popularity and aliases
consistently, then invalidate the event projection. Deployment and data repair
each require separate explicit approval. Track release state in
[FF-092](../todo.md#ff-092--losing-bridge-retires-a-kept-clip-that-the-winner-does-not-match).
