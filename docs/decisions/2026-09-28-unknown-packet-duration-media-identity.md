# Preserve media identity when packet duration is unknown

Status: implemented locally on `feat/event-media-identity`; production migration
and application rollout pending.

## Context

The [event-scoped media-identity decision](./2026-09-23-event-scoped-media-identity.md)
required ffprobe to report a duration for every encoded packet. In the first
production match day after rollout, many valid Twitter MP4s omitted
`duration_time` for every video packet. They still supplied packet payload
hashes, exact PTS/DTS values, and a stream time base, start, and duration.
The original conservative check correctly fell back to raw MD5, but it made
the new identity unavailable for most retained assets in that sample. Unit
fixtures and the original historical corpus all had packet durations, so
they did not exercise this source shape.

## Decision

- Keep `media-v1` byte-for-byte unchanged for clips with complete packet
  durations. Existing accepted assets and in-flight events must still match.
- Use `media-v2` only when at least one packet duration is unknown. It hashes
  every stream's existing codec/playback metadata plus exact stream time base,
  start PTS, and duration in time-base units; every packet contributes its raw
  PTS/DTS, encoded payload hash, size, side data, and either its reported
  duration or an explicit unknown marker. Separate audio/video packet order is
  preserved; cross-stream interleave and descriptive container tags are not.
- Require complete raw packet timestamps and a positive stream end boundary
  for every stream on the v2 path. If any are missing, keep the raw-MD5
  fallback. A different final-frame hold changes the stream duration and
  therefore the fingerprint.
- Accept both versions in the domain and database, keeping event-scoped
  uniqueness. Do not rewrite old assets, infer cross-version equality, or
  change stored MP4 bytes. A v1/v2 representation of otherwise identical
  media can remain a conservative false negative; dHash remains the later
  overlap path.

## Validation and rollout

Fake-backed tests cover metadata-only equivalence, packet/payload/timing
differences, missing end evidence, and a fixed v1 digest. A read-only
local-file regression against retained production bytes reproduced the stored
v1 digest exactly and produced v2 for previously failing clips. The additive
schema constraint migration must precede the worker rollout. Natural traffic
must then establish v2 coverage and any different-byte same-asset joins before
this issue can close. Historical NULL fingerprints are not backfilled by this
change.
