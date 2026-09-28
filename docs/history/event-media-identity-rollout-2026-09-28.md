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
