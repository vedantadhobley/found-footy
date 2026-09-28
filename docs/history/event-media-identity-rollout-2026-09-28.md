# Event-scoped media identity rollout — 2026-09-28

## Release

The user separately approved the production migration and application deploy
for `ee4eafb7affa88ae473fc00662793d4bd35064ec` on
`feat/event-media-identity`. The checkout was clean. The full engineering gate
(`make check`), Compose validation, and schema-hash comparison passed before
the release. Production previously ran `d256c7e`.

The quiet-window check found no scoped Firefox instances or current event
workflows. Only three future, non-playing fixtures remained in the active
table. The deploy script repeated its Firefox-fleet gate before recreation.

## Migration and application

`make migrate-prod` applied and verified
`20260923_01_add_event_media_identity.sql` at 05:05:55 UTC. Its checksum prefix
is `a73d641eb578`; the resulting schema-hash prefix is `895e2f641e13`.
Read-only SQL confirmed the ledger entry, `video_assets.media_fingerprint`,
`event_search_candidates.raw_md5`, and the
`video_assets_event_media_fingerprint` unique index. The migration adds no
historical fingerprint values and rewrites no accepted assets.

`make deploy-prod` built the exact commit, passed the non-root socket/scratch
permission smoke, recreated only the two workers, API, and Twitter service,
and verified all four process identities as `ee4eafb`. VNC remained stopped;
Postgres, Garage, and Temporal were not recreated.

## Initial verification and remaining boundary

All four application containers were running with zero restarts. A targeted
production REST fixture read succeeded, and fixture polls advanced to
05:20 UTC after the restart. No scoped Firefox instance or current event
workflow appeared in the first post-deploy matchday snapshot.

This establishes migration, startup, polling, and read-surface health. It does
not yet prove that a natural metadata-only follower joins its event's existing
encoded-media asset or that fingerprint probe time is acceptable. Measure
those on the next accepted candidate. FF-102 does not change clock sampling,
search precision, or the USA–Peru wrong-event admission.

Rollback to the older `d256c7e` binary is not a direct restart: that binary's
schema verifier does not know the newly recorded migration. Preserve the
ledger and use a reviewed compatible forward release or repair if needed.

## Unknown-packet-duration follow-up

The user separately approved the v2 production migration and application
deploy. In a quiet window with no scoped Firefox instances or running event
workflows, `make migrate-prod` applied
`20260928_01_accept_unknown_packet_duration_fingerprints.sql` at 21:30:34
UTC. Read-only verification found the schema hash
`6d72edc944798e7bcafe98e2ff91e1fffcee3d99b6429e7aaf72ac2e6a3d0dd3`
and a CHECK constraint accepting `media-v1` and `media-v2`. Existing asset
identities were not rewritten.

The separately approved `make deploy-prod` built and deployed
`626469349dcddf1d1441df1f7096a219b3f554a9`. Its second fleet gate
passed. Both workers, API, and Twitter reported the expected release identity;
all four were running with zero restarts. Worker startup verified the schema
and found all four existing Temporal schedules. API health and a targeted
fixture read returned HTTP 200. Postgres, Garage, and Temporal were not
recreated. Natural candidate traffic still needs to establish v2 probe
coverage and same-media joins; startup checks do not prove that behavior.
