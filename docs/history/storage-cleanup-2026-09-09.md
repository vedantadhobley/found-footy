# Legacy Twitter storage cleanup — 2026-09-09

The user approved deleting exactly `found-footy-prod-twitter-cookies` after a
read-only storage inspection. Docker reported no references from running or
stopped containers immediately before removal.

## Removed data

- Docker reported 100,054,676,159 bytes of file data. A read-only directory
  scan measured 102,027,169,792 allocated bytes, including 81,575,182,336 bytes
  under Firefox `cache2` directories.
- The volume contained 265 `firefox_profile*` directories. The archived
  Python session manager derived profile paths from container identity under
  persistent `/data`; replacing containers left previous profiles behind.
- `docker volume rm found-footy-prod-twitter-cookies` succeeded. A subsequent
  volume listing confirmed absence. No backup was created by this cleanup;
  the deleted legacy browser state cannot be recovered from this volume.

Current production Twitter remained running. It mounts the separate shared
cookie directory at `/config`; the current cookie file still existed after
removal. No current database, Garage volume, or other legacy volume was removed.
Python rollback must recreate its browser state or authenticate again rather
than assume that this legacy volume remains available.

## Separate current lifecycle finding

The deployed Twitter image `3723ce2aa6476a2c85e2bb24351336eda890683d`
declares `/data` as a Docker volume. The running static Twitter container has
an anonymous volume there, contrary to the documented writable-layer design.
Fleet release omits `RemoveVolumes`, so removing an event container does not
remove its anonymous profile volume. This is not the source of the deleted
legacy named volume and has not been fixed by deleting it.

Follow-up: [FF-090](../todo.md#ff-090--firefox-container-removal-leaves-anonymous-profile-volumes).
No anonymous-volume cleanup or runtime change was authorized or performed.
