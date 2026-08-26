# Exact-byte followers inherit the representative terminal result

## Context

FF-022 correctly moved exact MD5 ownership before dense hashing. Byte-identical
candidates share one hash and validation path, release redundant staging
objects, and contribute every sighting to popularity. The implementation also
recorded each follower as terminal `duplicate` as soon as the shared hash
succeeded or it matched a vision-pending representative.

That outcome was premature. Production retained eight duplicate rows for an
Awoniyi candidate cluster whose representative later failed the wrong-clock
vision gate. No asset promoted, so those rows were duplicates of no durable
winner. A wider cutover audit found 22 such rows across six events and no
corresponding post-download infrastructure failure or demonstrated unique clip
loss. The content-processing optimization was sound; its durable description
was not.

## Decision

For new EventWorkflow histories, a byte-identical candidate that joins a
representative before a durable asset exists is a follower, not yet a terminal
duplicate.

1. The follower contributes its popularity and releases its redundant staging
   object immediately. The representative retains the follower's tweet URL in
   deterministic workflow memory.
2. One representative still owns the cluster's hash, vision, and promotion
   retry units. Followers never multiply expensive validation work.
3. If the representative promotes, it becomes `promoted`; followers become
   `duplicate` and retain `winner_asset_id`.
4. If the representative collapses onto an existing asset after vision, it and
   its followers become `duplicate` with that asset's ID.
5. If vision deterministically rejects the representative, every member becomes
   `rejected` with the same reason and evidence.
6. If vision or promotion exhausts its retries, every member becomes `failed`
   with the same bounded reason.
7. A candidate that matches an asset which already exists remains an immediate
   duplicate because the winner is durable at match time.

Change ID `ff-065-exact-follower-outcome`, version 1, selects this command
sequence. Histories without the marker keep the former immediate follower
outcomes so Temporal replay remains deterministic.

## Consequences

- Exact-byte dedup keeps its current compute, staging, concurrency, and
  popularity benefits.
- `duplicate` once again means that a durable asset won the cluster.
- Rejected and failed clusters expose their real candidate population instead
  of mixing one verdict with several false winner references.
- The change needs no schema or frontend work. `outcome_detail` already carries
  winner evidence, and candidate outcomes are internal audit data.
- Historical ambiguous rows are not rewritten because they lack a durable
  candidate-to-representative link.

This decision refines the [FF-022 exact-byte ownership
contract](./2026-08-17-exact-md5-ownership-precedes-dense-hashing.md); it does
not replace its work-sharing or claimant-failover design.
