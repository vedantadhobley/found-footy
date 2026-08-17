# deployment.md — Go rebuild ledger

**Purpose.** Compose file reference + Caddy routing catalog + first-time
bootstrap steps for adapters that require operational provisioning
(NATS accounts, Garage cluster layout, etc.).

Cross-refs [`../rebuild-plan.md`](design/rebuild-plan.md) §10 for the full
deployment design. Divergences live in [`../decisions.md`](decisions.md).

**Update rule.** Any change to compose services, Caddy routes, first-time
bootstrap steps, or workflow scheduling updates this doc in the same
commit.

## Compose files

Both files are explicitly named — bare `docker compose` from this
directory has no default file and errors out, preventing a typo from
targeting prod. Every command must pass `-f docker-compose.{prod,dev}.yml`.

- `docker-compose.dev.yml` — hot-reload dev stack. The worker/api
  binaries run via `air` with source bind-mounted; twitter uses the
  reconciled `docker/twitter/Dockerfile` (Playwright base + driver
  install) without air — iterate via `docker compose build twitter`.
  The deployed binaries are worker, API, and Twitter; there is no scaler. Includes
  opt-in `twitter-vnc` service under `profiles: [vnc]` for manual cookie
  re-auth.
- `docker-compose.prod.yml` — **LIVE.** Runs the Go binaries — worker
  (`replicas: 2`) + api, both built from `./Dockerfile` — on Postgres +
  Garage. The cutover from the Python stack completed 2026-08-15 (La Liga
  match day). Twitter + twitter-vnc build from the reconciled
  `docker/twitter/Dockerfile`; the static `twitter` service is now the fleet's
  image-builder + single fallback, with per-event instances carrying
  the search load.

### Configuration contract

Compose remains the deployment owner: both stacks inject the repository's
gitignored `.env` and set environment-specific overrides such as the fleet
network and event subject token. Application code does not infer dev or prod.
At startup, `config.LoadFor` selects the sections owned by the worker, API, or
Twitter binary, parses only those variables, and validates semantic and
cross-field invariants before opening listeners, external connections, or a
browser. A malformed worker-only value therefore cannot stop the API, while
the worker rejects impossible values such as a search attempt beyond the
database range or an enabled fleet with no capacity.

`.env.example` is the canonical checked template, not a second source of
runtime defaults. `internal/config/contract_test.go` derives variable ownership
from Go struct tags and verifies the template, both Compose files, required
interpolation, explicit per-service overrides, environment-scoped fleet
network, worker-only `EVENT_ENV`, and cookie-directory mounts. New settings
must update their typed config, semantic validation when the type alone is not
enough, and the template or Compose route when operators must supply or
override them. Unknown legacy keys in a private `.env` are ignored; remove them
through a separately approved environment edit.

Production Compose also owns FF-021's fixed host-wide ffmpeg CPU contract. Its
32-thread budget is partitioned across the two workers as 16 concurrent
one-thread processes per replica. Explicit worker environment entries override
the single-worker `.env` defaults. The `x-ffmpeg-stack-budget` declaration and
the release contract test bind the budget, replica count, semaphore slots, and
per-process thread count together. Elastic worker counts require a shared
admission controller or dedicated Temporal queue; a process-local semaphore
cannot enforce a changing host-wide limit.

**Per-event fleet ownership:** Compose owns the deployment partition through
the explicit `found-footy-dev` / `found-footy-prod` network and passes its
actual name as `FIREFOXFLEET_NETWORK`. The Go provisioner treats that string as
an opaque scope; it has no dev/prod branch. Dynamic browsers are raw Docker API
children rather than Compose service replicas, so the provisioner must stamp
and enforce their ownership itself:

- daemon-global name: `<network>-firefox-ev-<full-event-uuid>` (for example,
  `found-footy-prod-firefox-ev-<uuid>`), matching the workspace
  `<project>-<env>-<role>` convention;
- ownership label: `found-footy.fleet.scope=<network>`;
- network-local workflow alias: `ff-firefox-ev-<event-uuid-prefix>`.

The deterministic name locates a specific event container. Capacity, listing,
reaping, and release still require the fleet label, scope label, and configured
network; a matching name alone is not deletion authority. Lifecycle operations
inspect that ownership before starting or deleting the container. This lets dev
and prod share one Docker daemon without either stack seeing or removing the
other's browsers, while preserving existing Temporal workflow addresses.

Each dynamic event container carries Docker `restart: on-failure` (FF-017).
Firefox/context loss makes the Go service publish failed health and exit PID 1
non-zero; Docker then rebuilds the process unit in the same container from the
shared cookie backup. Explicit fleet release uses Docker stop/remove and does
not trigger the restart policy. Compose-managed headless Twitter retains
`restart: unless-stopped`; the opt-in VNC service intentionally retains
`restart: no` because the operator owns that session.

If a legacy `ff-firefox-ev-*` container appears, stop before deployment and
identify its workflow and network ownership. The scoped provisioner
intentionally ignores legacy unscoped containers because it cannot prove which
stack owns them; running old and new containers together could also duplicate
the preserved network alias. Any legacy removal remains a separate, explicitly
approved production action.

**Twitter image shape** (per decisions.md 2026-07-22): one Dockerfile
serves both `twitter` (headless) and `twitter-vnc` (visible display).
Runtime branches on `TWITTER_VNC_MODE=true` env var —
`docker/twitter/entrypoint.sh` boots Xvfb + fluxbox + x11vnc +
websockify+noVNC before exec'ing the binary when set. Build-time
`WITH_VNC=true` arg gates the ~150 MB of VNC binaries so the headless
image stays lean. See the [one-Dockerfile decision](decisions.md#2026-07-22--twitter-dockerfile-one-file-with_vnc-gated-matches-pythons-shape)
for full rationale.

**Twitter VNC one-command flow:**

```bash
make twitter-vnc-up     # brings up twitter-vnc via `docker compose --profile vnc up`
# operator logs in at http://found-footy-dev-twitter-vnc.luv (dev)
make twitter-vnc-down   # stops + removes the container when done
```

The production `/authenticate` response advertises the explicit repository
command `docker compose -f docker-compose.prod.yml --profile vnc up -d
twitter-vnc`. A bare `docker compose` command is invalid here because the
repository deliberately has no default Compose file (FF-018).

**Cookie storage + host perms (load-bearing — host state, NOT in the repo).**
The shared Twitter cookie file lives on the host at `FIREFOXFLEET_COOKIE_HOST_PATH`
(default `~/.config/found-footy/twitter_cookies.json`), bind-mounted into `twitter`,
`twitter-vnc`, and the per-event fleet containers. Two requirements — get either
wrong and the cookie write-back / VNC re-auth silently fails to persist (the write
error is swallowed; see [decisions.md](decisions.md) 2026-08-15):

1. **Mount the parent DIR, not the file:** `~/.config/found-footy:/config`. `rename(2)`
   onto a single-file bind mountpoint returns EBUSY, and the backup writes atomically
   (temp + rename).
2. **The dir must be group-writable by the container user.** Playwright runs as
   `pwuser` (uid 1001), a member of group `users` (gid 100):
   ```bash
   chgrp users ~/.config/found-footy && chmod 775 ~/.config/found-footy
   ```
   A host re-provision MUST redo this, or refreshes + re-auths never persist.

Cross-project external networks that must exist before either stack
comes up (created once at workspace bootstrap):

```bash
docker network create luv-dev
docker network create luv-prod
docker network create proxy
```

Caddy routes live centrally at `~/workspace/proxy/caddy/caddy.d/found-footy.caddy`
until the workspace-owned migration changes that contract. The Go read API
is fronted at `found-footy-<env>-api.<BASE_DOMAIN>` → `reverse_proxy
found-footy-<env>-api:8081` — the Chi read surface; `:8080` is internal
metrics/healthz, never exposed. The in-repo `caddy/found-footy.caddy` is the
documentation copy (not read by Caddy).

Every Go binary must bind its configured internal metrics/health address
before it starts application work (FF-026). Port conflicts fail the container
non-zero, and a later listener failure cancels and drains the binary so the
Compose restart policy can replace it. A running container without its
`/metrics` and `/healthz` listener is not a supported degraded state.

## Prod image hardening (non-root)

The prod image (`./Dockerfile`, worker + api) runs **non-root** — `adduser
--uid 1000 app` + `USER app`. Two runtime writes that dev (root) got for free
then need explicit grants; both bit the 2026-08-15 cutover before they were
added:

- **docker.sock for the Firefox fleet.** The worker provisions/releases per-event
  Firefox via the Docker API, so it must write `/var/run/docker.sock`
  (root:docker on the host). The host docker gid can't be baked into the image,
  so the prod compose grants it at runtime: `group_add: ["984"]` (luv's docker
  gid). Missing → every Provision/Release/Reap is "permission denied" and no
  clip is ever searched. luv-specific; reparameterize if prod moves host.
- **`/scratch` for video staging.** `app` can't `mkdir` under root-owned `/`,
  so the image bakes the dir it needs: `mkdir -p /scratch && chown app:app
  /scratch` (`VIDEO_SCRATCH_DIR` default). Keeps it a COMPLETE non-root image —
  no runtime chown / env-redirect hack.

**Deploy gate:** `scripts/smoke_prod_perms.sh [image]` (default
`found-footy-worker:latest`) runs the real prod worker image as `--user app
--group-add <host-docker-gid>` and asserts both — docker.sock writable +
`/scratch` writable — failing loud so a half-configured non-root image can't
ship. Run it before a prod deploy; it's the check that would have caught the
cutover perm bugs.

## First-time dev bootstrap

Some adapters require one-time operational setup on a fresh dev host
before the corresponding Go binary can connect. Data lives in named
Docker volumes → surviving `docker compose down`, wiped on
`docker compose down -v`.

### Postgres

Automatic. `internal/infra/pg/schema.sql` is bind-mounted into
`/docker-entrypoint-initdb.d/01_init.sql`; Postgres runs it on first
startup with an empty volume. Re-provision: `docker volume rm
found-footy-dev_postgres-data` then `docker compose up -d postgres`.

**Drift guard (audit P0-3).** `pg.VerifySchema` runs at worker/api startup:
first boot on a DB stamps the embedded `schema.sql` SHA-256 into a
`schema_version` row; later boots compare and **refuse to start on a
mismatch**. So an edit to `schema.sql` that never reached this DB fails loud
instead of silently no-opping. After an *intentional* in-place schema change
(rare — the norm is edit + re-provision), re-stamp:
`UPDATE schema_version SET schema_hash = '<new sha256 of schema.sql>'`, or just
wipe + reprovision (a fresh volume auto-stamps). No migration files — see
[decisions.md](decisions.md) 2026-08-13 (audit P0-3) for why.

### NATS

Currently runs no-auth (workspace NATS's `nats.conf` has the accounts
block commented out). No creds needed for dev. When workspace accounts
land via `nsc`, restore the creds mount + populate `NATS_CREDS_FILE`
in `.env`.

### Garage (S3-compatible blob storage)

One-time setup after `docker compose up -d garage`:

```bash
# 1. Get the node ID.
NODE=$(docker exec found-footy-dev-garage /garage status \
  | grep -oE '^[0-9a-f]{16}' | head -1)

# 2. Assign a cluster role to the node.
docker exec found-footy-dev-garage /garage layout assign \
  "$NODE" -z dc1 -c 10GB -t dev

# 3. Apply the staged layout (version 1 = first commit).
docker exec found-footy-dev-garage /garage layout apply --version 1

# 4. Create the app bucket.
docker exec found-footy-dev-garage /garage bucket create found-footy

# 5. Mint an access key.
docker exec found-footy-dev-garage /garage key create found-footy-key

# 6. Grant the key read+write+owner on the bucket.
docker exec found-footy-dev-garage /garage bucket allow \
  --read --write --owner found-footy --key found-footy-key

# 7. Fetch the secret (only visible via --show-secret).
docker exec found-footy-dev-garage /garage key info --show-secret found-footy-key
```

Copy the `Key ID` and `Secret key` from step 7 into `.env`:

```
S3_ACCESS_KEY_ID=GK...
S3_SECRET_ACCESS_KEY=...
```

Restart worker + api (`docker compose up -d --force-recreate worker api`)
— you should see `s3_connected` on both. Re-provision the whole cluster:
`docker volume rm found-footy-dev_garage-{data,meta}`.

#### Garage (prod)

Same procedure against `found-footy-prod-garage`, with two prod deltas:

1. Config is the shared `garage.toml` (committed, secret-free) — the same file
   dev uses. The 3 garage secrets are injected from `GARAGE_*` in the gitignored
   `.env`; there is no per-env garage config file.
2. **Import** dev's S3 key rather than minting a fresh one, so the shared
   `.env` `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` authenticate against
   prod too. Each Garage is a separate instance — sharing the key *value*
   grants no cross-instance access; both are internal, single-node.

```bash
NODE=$(docker exec found-footy-prod-garage /garage status \
  | grep -oE '^[0-9a-f]{16}' | head -1)
docker exec found-footy-prod-garage /garage layout assign "$NODE" -z dc1 -c 50GB -t prod
docker exec found-footy-prod-garage /garage layout apply --version 1
docker exec found-footy-prod-garage /garage bucket create found-footy
# Import dev's key (values sourced from .env) instead of `key create`:
docker exec found-footy-prod-garage /garage key import --yes -n found-footy-key \
  "$S3_ACCESS_KEY_ID" "$S3_SECRET_ACCESS_KEY"
docker exec found-footy-prod-garage /garage bucket allow \
  --read --write --owner found-footy --key found-footy-key
```

No `.env` change — the imported key matches what's already there. Worker +
api should log `s3_connected`. Reprovision:
`docker volume rm found-footy-prod_garage-{data,meta}`.

## Deploy tracking

`make deploy-prod` is the repository-owned application release command. It is
a production mutation and requires explicit approval for that invocation. It:

1. resolves the full SHA of the clean commit already checked out and one UTC
   build time; it does not fetch, pull, or switch branches;
2. refuses uncommitted or untracked files, then rechecks HEAD and tree
   cleanliness immediately before recreation;
3. selects running event browsers by the fleet label plus production-network
   membership, and refuses rollout both before the build and immediately
   before application recreation;
4. builds worker, API, Twitter, and Twitter VNC with that `GIT_SHA`, `BUILT_AT`,
   and full-SHA `IMAGE_TAG`, then runs the non-root worker permission smoke;
5. recreates worker, API, and static Twitter without touching durable
   dependencies; if VNC was already running, it recreates VNC too; and
6. verifies the two workers and API through
   `found_footy_deploy_git_sha_info{binary,git_sha,image_tag,built_at}`, and
   verifies Twitter plus an already-running VNC through `/status.build`.

Worker and API also put the identity in their startup log. Twitter puts it in
`service_starting`. `FIREFOXFLEET_IMAGE` uses the same full-SHA Twitter tag, so
new event browsers cannot drift from the worker release. The Compose defaults
remain `unknown`/`latest` so the model can be inspected without release
variables; those defaults are not a valid production release.

The exact contract and its divergence from the historical deployment sketch
are recorded in the
[immutable-release decision](./decisions/2026-08-16-immutable-production-release-identity.md).

## Workflow scheduling

The former MonitorWorkflow was split into two independent schedules on
2026-07-11; the combined poll no longer exists. Both are registered on
worker startup via `ensureActivePollSchedule` + `ensureStagingPollSchedule`
in `cmd/worker/main.go`, idempotently — Create swallows
`ErrScheduleAlreadyRunning`. Startup does not reconcile changed definitions;
that defect is [`FF-009`](./todo.md#confirmed-lower-priority-backlog).

**ActivePollWorkflow** — every 30 seconds. Schedule `active-poll-scheduled`.

- Interval: 30 seconds (via `ScheduleIntervalSpec` from
  `WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL`, default `30s` — cron can't do
  sub-minute resolution)
- Overlap: `SCHEDULE_OVERLAP_POLICY_SKIP` — if the prior cycle is still
  running when the next tick fires, skip. Better than double-fanning-out
  reconcile activities.
- Args: empty `ActivePollWorkflowInput{}` — a zero `ActivationWindow` falls
  back to config (`WORKFLOWS_ACTIVATION_WINDOW`, default 5m).

**StagingPollWorkflow** — every 15 minutes. Schedule `staging-poll-scheduled`.

- Cron: `*/15 * * * *` (`WORKFLOWS_STAGING_POLL_CRON`, runtime-tunable via
  `temporal schedule update staging-poll-scheduled --cron ...`)
- Overlap: `SCHEDULE_OVERLAP_POLICY_SKIP`
- Args: empty `StagingPollWorkflowInput{}` — same zero-`ActivationWindow`→
  config fallback (default 5m).

When no active fixtures exist, an ActivePoll cycle completes early after
`ListActiveFixtureIDs → []`.

**IngestWorkflow** — daily at 00:05 UTC. Registered on worker startup
via `ensureIngestSchedule` in `cmd/worker/main.go`.

- Schedule ID: `ingest-scheduled-daily`
- Cron: `5 0 * * *` (00:05 UTC)
- Overlap policy: `SCHEDULE_OVERLAP_POLICY_SKIP` (if a prior run is
  still executing, skip this one)
- Args: `IngestWorkflowInput{RetentionDays: 14}` — plan §5 W1
  default retention

Idempotent: on subsequent worker restarts, the create call returns
`temporal.ErrScheduleAlreadyRunning` and the code logs an `already
exists` action rather than erroring. Manual updates via `temporal
schedule update` are safe; the startup code does NOT overwrite them.

Verification: `docker exec found-footy-dev-temporal temporal --address
temporal:7233 schedule list` shows the schedule with its next run
time. Schedules survive worker restarts (state lives in Temporal
server, not on the worker).

**EventWorkflow** — NOT scheduled. Monitor's `ReconcileFixture` spawns it
Temporal-direct (client `StartWorkflow`, deterministic ID `event-{id}`) when a
goal's `downstream_triggered` flips (2026-07-16). See
[orchestration.md](./orchestration.md) for the spawn + completion contract.

**Manual trigger** for ad-hoc re-ingest (e.g. testing after a code
change) still works:

```bash
docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/trigger_ingest'
```

This bypasses the schedule entirely and fires a one-off workflow run
against the current worker.

Cross-refs:
- [Plan §10 (deployment)](design/rebuild-plan.md#10-deployment) — full deployment spec
- [orchestration.md](./orchestration.md) — workflow inventory + wire-up
- [temporal.md](./temporal.md) — Client/Worker adapter shape
