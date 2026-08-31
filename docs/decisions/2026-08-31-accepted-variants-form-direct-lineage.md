# Accepted variants form direct lineage, not perceptual clusters

## Context

Atomic placement retained only an accepted candidate that became a public
winner. When a distinct MD5 passed vision but lost a dHash/quality comparison,
the transaction credited its sources directly to the incumbent and the
activity deleted its staging bytes. Post-hoc review therefore could not inspect
the losing presentation, reconstruct its exact source votes, or evaluate why
the keeper decision was made.

dHash matching is not transitive. One candidate can directly match two clips
that do not match each other, and reviewed components contain both true
duplicates and distinct broadcasts or edits. A connected component is
therefore evidence topology, not a content-identity class.

## Decision

Every vision-accepted distinct MD5 becomes one durable `video_assets` node in
its event. Placement records only direct decisions:

- a losing observed variant points through `superseded_by` to the selected
  live root;
- an accepted candidate keeps an immutable `observed_asset_id` for the bytes
  it carried and a movable `credited_asset_id` for the current root;
- exact-byte frequency derives from candidates grouped by immutable
  `observed_asset_id`, while the live root carries aggregate credited
  popularity used by public ranking;
- only live roots receive active public shares and appear in event reads;
- recovery loads every event asset so any accepted MD5 can resolve through its
  committed edge chain to a live exact-alias root.

The graph is never closed transitively and has no perceptual `cluster_id`.
Public selection follows committed edges to roots; it does not infer a new
edge or duplicate decision from graph connectivity.

Accepted losing bytes remain in Garage through the existing FF-079 public
media window. Ordinary retention then reclaims every event asset together and
keeps the SQL nodes, hashes, metadata, attribution, and edges as permanent
audit evidence. Placement no longer performs immediate loser-object deletion.

The workflow payload change is gated by
`ff-083-accepted-variant-evidence`. Existing histories retain their prior
command payloads.

## Consequences

New evidence can reproduce direct dHash relationships and compare every
accepted presentation, including variants that were never public. Storage use
increases only inside the already bounded media-retention window. Historical
losing candidates cannot be reconstructed because their hashes and bytes were
not retained.

The schema adds nullable `event_search_candidates.observed_asset_id`; old,
rejected, and failed candidate rows remain valid without it. A hidden variant
does not receive a fabricated public share ID.
