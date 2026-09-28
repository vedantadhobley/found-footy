# Event-scoped encoded-media identity

Status: deployed to production on 2026-09-28. The first match day exposed
valid clips without packet durations; the [follow-up decision](./2026-09-28-unknown-packet-duration-media-identity.md)
adds a conservative, separately versioned path for them.

## Context

Several retained Barcelona uploads had different whole-file MD5s but identical
encoded video, audio, packet timing, and playback configuration. MP4 creation
and modification timestamps alone changed. Treating each as a distinct asset
wasted dense hashing and vision calls, retained redundant Garage objects, and
split exact-source support. Conversely, several clips with identical sampled
dHash traces had different encoded or decoded pictures. A video-only digest or
metadata-blind container rewrite would conflate evidence we need to keep.

## Decision

- Keep raw-file MD5. Do not rewrite or strip the MP4 stored in Garage.
- Compute a versioned SHA-256 fingerprint from every encoded audio/video
  stream's codec/playback configuration, packet payload hashes and presentation
  timing. Ignore descriptive container tags and cross-stream packet interleave.
  Missing or unsupported evidence yields no media fingerprint and safely falls
  back to raw MD5.
- Claim and persist this identity only within one event. `(event_id,
  media_fingerprint)` is unique for non-null values. The same media found for
  another goal retains its own asset, validation, support, and object path.
- A metadata-only follower reuses the accepted event asset. Each accepted
  candidate still records its own raw MD5 and contributes one source vote.
  dHash remains the later, fuzzy overlap signal for re-encodes and crops; it
  does not establish exact media identity.
- Gate the new activity payload and claim path with a Temporal version marker.
  Existing histories keep their old command sequence. Legacy assets have null
  media fingerprints; no historical object is rewritten or silently merged.

## Evidence and tradeoffs

The local test compared five Raphinha 22′ retained copies, two additional
timestamp-only pairs, four visually similar but nonidentical pairs, and four
synthetic controls. Timestamp/title-only copies joined; changed rotation,
audio presence, video content, and packet timing remained distinct. The added
ffprobe pass reads packet metadata before staging, so it saves downstream
hash/vision/accepted-object work but not the original download or staging
upload. A conservative false negative costs work; a false positive would lose
evidence, so incomplete probes never claim equality.

Existing FF-083 assets still use their representative raw MD5 for deterministic
UUIDs and storage keys. The media fingerprint aliases that representative, not
an event-independent object. FF-081's weighted/coverage graph remains a
separate selection problem; this decision only removes redundant exact-media
nodes from new accepted event histories.
