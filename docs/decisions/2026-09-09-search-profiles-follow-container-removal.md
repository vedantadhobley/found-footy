# Search profiles follow container removal

## Context

FF-090 found a mismatch between the disposable-profile design and the image:
`docker/twitter/Dockerfile` declared `/data` as a volume. Docker consequently
created an anonymous volume for each search container. Fleet removal omitted
`RemoveVolumes`, leaving that storage behind after container removal.

The [July 23 decision](./archive-through-2026-08-16.md#2026-07-23--twitter-firefox-profile-ephemeral-per-container-python-shape-not-shared-volume)
intended writable-layer isolation but incorrectly described both Python
storage and container restart semantics. The September 9
[legacy cleanup](../history/storage-cleanup-2026-09-09.md) confirmed Python
had accumulated persistent per-container profile directories. A container
restart preserves its writable layer; neither a crash nor restart removes it.

## Decision

- The headless image declares no `VOLUME` paths. Its private profile remains
  at `/data/firefox-profile` in the container writable layer.
- Compose and the fleet explicitly bind only the shared cookie directory at
  `/config`. Cookies persist independently of search-container replacement.
- Operator login retains its separate image and explicit named profile mount.
- Owned fleet-container removal includes `RemoveVolumes: true` to remove
  anonymous volumes attached to old-image containers. Docker does not remove
  named volumes or bind-mount data through this option.
- Start failure still retains the stopped container for provisioning retries;
  normal release or the existing reaper removes it with its disposable storage.
- No daemon-wide prune, profile registry, new scheduled sweeper, tmpfs budget,
  workflow version, or database migration is introduced.

## Verification and rollout

Unit tests pin removal options, failed-start recovery, repeated release,
removal-error propagation, and deployment-scope isolation. An isolated Docker
test proves old anonymous storage disappears while explicit named storage
survives. Inert Compose tests pin both environments' cookie and VNC mounts.

`scripts/smoke_twitter_storage.sh` checks final image metadata and uses isolated
Compose sentinels to test removal of a legacy `/data` mount, restart versus
replacement, cookie persistence, and explicit login-profile persistence. It
never launches a real browser or reads authentication credentials.

Roll out worker and headless Twitter together through the separately approved
release workflow. Verify static and newly created event browsers have no
`/data` mount. Removing the image declaration does not reclaim volumes already
detached from deleted containers; existing orphan cleanup requires evidence
of ownership and separately approved exact targets.

FF-073's reaper error aggregation and lease policy remain separate work. This
change does not claim to make failed Docker deletions transactional or atomic.
