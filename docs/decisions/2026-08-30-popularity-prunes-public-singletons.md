# Popularity prunes public singleton clips asymmetrically

## Context

An event can accumulate many distinct clips backed by only one accepted source.
Once repeated sources establish a stronger clip, returning every singleton adds
noise without improving the default presentation. Popularity is still weaker
evidence than timestamp verification: a widely reposted unverified clip can be
the wrong moment, a meme, or unrelated footage.

The durable video model must retain accepted clips. Popularity can change, an
asset can be superseded or removed, and an existing share URL must remain
stable. Visibility therefore cannot be a terminal share state.

## Decision

FF-078 makes singleton pruning a derived API read policy:

- A timestamp-verified active clip with popularity at least three suppresses
  every active popularity-one clip for that event.
- An unverified active clip with popularity at least three suppresses only
  unverified popularity-one clips. Timestamp-verified singletons remain public.
- Popularity-two clips remain public.
- The query applies visibility before assigning public rank, so returned ranks
  remain contiguous.
- Suppressed shares and assets retain their durable state and direct playback
  behavior. The rule is recalculated from the current live set on every read.

This policy applies to the event projection used by full fixture snapshots,
targeted fixture reads, and targeted event reads. Existing `event.video`
invalidation causes consumers to refetch after a popularity-changing placement.

## Consequences

The change needs no schema, workflow, or frontend implementation update. If a
threshold clip leaves the live set, previously suppressed singletons can appear
again. An unverified popularity winner cannot displace the only verified
alternative, limiting—but not eliminating—the risk that repost frequency
amplifies semantically wrong footage.
