# Public event presentation is producer-owned

## Decision

Found Footy's read API owns provider-independent event and competition
semantics. Vedanta Systems remains responsible for transport recovery,
user-timezone bucketing, ordering explicit fields, and visual composition.

The additive contract supplies:

- event `kind` and normalized presentation label;
- complete event `presentation_state`;
- event team side and score-before/score-after context;
- normalized competition round label and kind;
- producer-owned competition priority.

The video `share_id` remains authoritative. The existing `phase` remains during
the additive cutover, but it is deliberately coarser than presentation state:
its `detected` value covers both an unknown-player placeholder and a known
player being confirmed. New consumers use `presentation_state` directly rather
than combining phase with player nullability. The BFF must not generate
Python-era completion flags or parse media identity from a URL.

## Score integrity

Score context is emitted only when the surviving stored goal inventory exactly
matches the provider's current home/away score. A partial or degraded event
feed produces null score context. This fails closed instead of publishing an
invented event scoreline.

API-Football describes its event response as chronological, but Found Footy's
stored event row does not retain the provider-array position. Events discovered
in one poll also share one `first_seen_at`. When multiple goals have the same
minute and stoppage minute, neither first observation nor UUID proves their
relative order. Those goals receive null score context. Their aggregate score
still advances the boundary used by later unambiguous goals. A future durable
provider ordinal may restore the tied scorelines; deterministic storage order
must never stand in for that evidence.

A directly requested removed goal can expose its tentative boundary for
historical context, but it never advances the score assigned to later surviving
events. The event-only endpoint loads the parent fixture and surviving siblings
so its projection matches a fixture snapshot.

## Ordering and recovery boundaries

No opaque fixture or event sort key is introduced. Fixtures already carry the
backend-derived `presentation_state` and `last_activity_at`; React may sort
those explicit fields. Fixture event arrays carry deterministic match-clock
order and may be reversed for recent-first display. Equal-clock tie order is
not semantic until a durable provider ordinal exists.

New event membership normally arrives through `fixture.update`. Because Core
NATS can lose that dirty signal, a later `event.update` whose event or parent is
missing locally still requires an authoritative parent-fixture refetch. A
consumer must not discard the update or append an unordered partial event.

User-local and UTC date grouping remains in React. The BFF does not own user
timezone presentation.

## Compatibility

The producer fields are additive. Existing event `type`, `detail`, raw round,
phase, video, and fixture fields remain during the consumer cutover. No schema
migration or NATS payload change is required. Once Vedanta Systems consumes the
new projection, it can remove legacy underscore flags, fabricated timestamps,
provider-string parsing, and URL-derived share identity independently.
