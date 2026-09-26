# Raphinha and Cancelo legacy media inventory — 2026-09-23

Read-only production inventory for FF-102. The scope is Raphinha's 22′ and 52′
goals against Sevilla (fixture `1570400`) and Cancelo's 8′ goal against Racing
Santander (fixture `1570385`). No production row, Garage object, service, or
configuration was changed.

## Method

SQL selected assets with identical complete stored dHash traces inside each
event. Sixteen retained MP4s in those candidate sets were read through the
GET-only operator UI: nine were already local from the prior review and seven
were read for this inventory. A freshly compiled FF-102 `media-v1` probe then
hashed every file's encoded audio/video streams, packet payloads, timing, and
playback configuration. The old temporary test binary was stale: it had split
Raphinha's public 22′ root from five hidden copies. Recompiling from commit
`a978ff7` placed all six in one group. The checked-in opt-in corpus harness
now asserts this six-file group and three other reviewed groups when supplied
all sixteen local files through `FF_MEDIA_FINGERPRINT_HISTORICAL_DIRS`. The
normal unit suite tests the stream/packet rules with fake ffprobe output.

Each of these sixteen assets has one accepted candidate observation and one
retained verified validation. Every object is still present and unreclaimed.
The groups are event-scoped; equal media in different events is never combined.

## Verified groups

| Event | Exact-media group | Assets | Existing public/link state | Redundant bytes if one object remains |
|---|---|---:|---|---:|
| Raphinha 22′ | `media-v1:badc46a78d80…` | 6 × 4,238,434 B | `15e58928` has the active share; the other five are shareless | 21,192,170 B |
| Raphinha 52′ | `media-v1:3c449000c475…` | 4 × 4,988,339 B | All four are hidden/shareless under another selected root | 14,965,017 B |
| Cancelo 8′ | `media-v1:a54e671b759a…` | 4 × 5,982,568 B | All four are hidden/shareless under another selected root | 17,947,704 B |
| Cancelo 8′ | `media-v1:95a82f58bab5…` | 2 × 2,008,644 B | Both are superseded; `6330a03b` anchors an existing superseded share | 2,008,644 B |

Total potential early reclaim in this scoped inventory: **56,113,535 B
(53.51 MiB)**. That is object-storage space, not a reduction in source
observations or SQL audit rows. The Raphinha 22′ public object itself is an
exact-media member and must remain playable. The other groups each need one
retained media exemplar if bytes are compacted before routine retention.

Exact asset UUIDs by group:

- Raphinha 22′: `0229a3ad-68c3-5e39-96f8-96fbdb23ede9`,
  `07814816-b574-527c-8431-8ece98f25851`,
  `0b18843c-a844-5949-88d6-920d897bcd8d`,
  `15e58928-4a5d-5d28-a7ac-f0d4ddf91226`,
  `925f4142-ab7e-5600-bf26-b0359357e8b5`,
  `b491bc31-23b0-57fa-a256-e2597ca4f99e`.
- Raphinha 52′: `73037433-6120-5388-84a1-f14abb3e083f`,
  `77115242-c6e7-5022-8839-33a097abadbc`,
  `96f350e7-ba60-5fde-ba3d-4272961611f6`,
  `a7bd7843-0b3a-560f-b61b-f13ca6879492`.
- Cancelo 8′, long: `20050567-e980-5230-840d-08973e0834f9`,
  `9204a3c3-de56-5050-b9fc-18eab761a9da`,
  `98bd868d-52ef-513d-8fb4-1edbd9674937`,
  `de940c5d-c2d3-58f8-bb45-a9e2cf5998b2`.
- Cancelo 8′, short: `3b66373a-3a74-5559-9087-4d38192a282d`,
  `6330a03b-5459-5e6a-802a-39112d9f8037`.

Four other inspected pairs (Anthony 61′, Messi 62′, Cherki 32′, Yamal 45′)
had identical sampled dHash traces but **different** media fingerprints. They
are not exact-media cleanup candidates. This distinction is why an SQL-only
`frame_hashes` grouping must remain a shortlist, never deletion authority.

## Cleanup boundary

This inventory is not authorization to mutate production. Do not delete the
duplicate SQL assets: candidate `observed_asset_id`, accepted validations,
supersession lineage, and one superseded share still reference them. The public
share resolver follows the lineage; the operator UI's `/media/{asset_id}`
instead reads that exact object and would show `410` if it were reclaimed.
FF-081's selection planner also treats reclaimed hidden assets as ineligible
for restoration. Any early object compaction therefore needs a tested,
event-locked operation with an exact keeper choice, share/link checks, direct
support and selection simulation, idempotent Garage deletion and reclamation
marking, and an operator-UI representation of retained evidence with shared
media. The partial unique `(event_id, media_fingerprint)` index cannot simply
be backfilled onto all of these legacy rows.

Routine FF-079 retention already reclaims all media after its fixture exits
the public window of fourteen distinct completed-fixture dates while retaining
SQL audit and share rows. Given only 53.51 MiB here, automatic forward
prevention and normal retention are lower-risk than a one-off production
rewrite. A broader approved inventory would be needed to justify a general
historical compactor on storage savings alone.
