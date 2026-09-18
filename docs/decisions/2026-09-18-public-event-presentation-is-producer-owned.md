# Public event presentation is producer-owned

## Decision

Found Footy's read API owns provider-independent event and competition
semantics. Vedanta Systems remains responsible for transport recovery,
user-timezone bucketing, ordering explicit fields, and visual composition.

The additive contract supplies:

- event `kind` and normalized presentation label;
- event team side and score-before/score-after context;
- normalized competition round label and kind;
- producer-owned competition priority.

The existing direct `phase` and video `share_id` remain authoritative. The BFF
must preserve them instead of generating Python-era completion flags or parsing
media identity from a URL.

## Score integrity

Score context is emitted only when the surviving stored goal inventory exactly
matches the provider's current home/away score. A partial or degraded event
feed produces null score context. This fails closed instead of publishing an
invented event scoreline.

A directly requested removed goal can expose its tentative boundary for
historical context, but it never advances the score assigned to later surviving
events. The event-only endpoint loads the parent fixture and surviving siblings
so its projection matches a fixture snapshot.

## Ordering and recovery boundaries

No opaque fixture or event sort key is introduced. Fixtures already carry the
backend-derived `presentation_state` and `last_activity_at`; React may sort
those explicit fields. Fixture event arrays carry authoritative match-clock
order and may be reversed for recent-first display.

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
