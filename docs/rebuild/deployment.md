# deployment.md — Go rebuild ledger

**Purpose.** Compose file reference + Caddy routing catalog + first-time
bootstrap steps for adapters that require operational provisioning
(NATS accounts, Garage cluster layout, etc.).

Cross-refs [`../rebuild-plan.md`](../rebuild-plan.md) §10 for the full
deployment design. Divergences live in [`../decisions.md`](../decisions.md).

**Update rule.** Any change to compose services, Caddy routes, first-time
bootstrap steps, or workflow scheduling updates this doc in the same
commit.

## Compose files

Both files are explicitly named — bare `docker compose` from this
directory has no default file and errors out, preventing a typo from
targeting prod. Every command must pass `-f docker-compose.{prod,dev}.yml`.

- `docker-compose.dev.yml` — hot-reload dev stack. worker/api/scaler
  binaries run via `air` with source bind-mounted; twitter uses the
  reconciled `docker/twitter/Dockerfile` (Playwright base + driver
  install) without air — iterate via `docker compose build twitter`.
  Includes opt-in `twitter-vnc` service under `profiles: [vnc]` for
  manual cookie re-auth.
- `docker-compose.prod.yml` — currently still runs the Python codebase.
  Will migrate to Go binaries during Phase M/C cutover per rebuild-plan.md
  §13. Twitter + twitter-vnc services already wired to the reconciled
  Dockerfile as of 2026-07-22 (previously referenced a broken top-level
  Dockerfile that would have crashed on `playwright.Run()`).

**Twitter image shape** (per decisions.md 2026-07-22): one Dockerfile
serves both `twitter` (headless) and `twitter-vnc` (visible display).
Runtime branches on `TWITTER_VNC_MODE=true` env var —
`docker/twitter/entrypoint.sh` boots Xvfb + fluxbox + x11vnc +
websockify+noVNC before exec'ing the binary when set. Build-time
`WITH_VNC=true` arg gates the ~150 MB of VNC binaries so the headless
image stays lean. See [decisions.md](../decisions.md#2026-07-22)
for full rationale.

**Twitter VNC one-command flow:**

```bash
make twitter-vnc-up     # brings up twitter-vnc via `docker compose --profile vnc up`
# operator logs in at http://found-footy-dev-twitter-vnc.luv (dev)
# or http://found-footy-prod-twitter-vnc.luv (prod)
make twitter-vnc-down   # stops + removes the container when done
```

Cross-project external networks that must exist before either stack
comes up (created once at workspace bootstrap):

```bash
docker network create luv-dev
docker network create luv-prod
docker network create proxy
```

Caddy routes live centrally at `~/workspace/proxy/caddy/caddy.d/found-footy.caddy`
until the glob-import migration lands per workspace TODO.

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

## Deploy tracking

- Every binary bakes `gitSHA` + `builtAt` via `-ldflags "-X main.gitSHA=... -X main.builtAt=..."` at container build time.
- Values surface in the `found_footy_deploy_git_sha_info{binary,git_sha,image_tag,built_at}` gauge + the `startup` log line.

## Workflow scheduling

**MonitorWorkflow** — every 30 seconds. Registered on worker startup
via `ensureActivePollSchedule` + `ensureStagingPollSchedule` in `cmd/worker/main.go` (O2).

- Schedule IDs: `active-poll-scheduled` (30s IntervalSpec) + `staging-poll-scheduled` (cron `*/15 * * * *`)
- Interval: 30 seconds (via `ScheduleIntervalSpec` — cron doesn't
  support sub-minute resolution)
- Overlap: `SCHEDULE_OVERLAP_POLICY_SKIP` — if the prior cycle is
  still running when the next tick fires, skip. Better than
  double-fanning-out reconcile activities.
- Args: empty `MonitorWorkflowInput{}` — workflow self-configures
  with 30-min default ActivationWindow.

Verified live: cycles firing every 30s exactly. When no active
fixtures exist (e.g. before today's ingest ran), workflow completes
early after `ListActiveFixtureIDs → []`.

**IngestWorkflow** — daily at 00:05 UTC. Registered on worker startup
via `ensureIngestSchedule` in `cmd/worker/main.go` (O1e/b).

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

**ActivePollWorkflow + StagingPollWorkflow** — schedules registered (DiscoveryWorkflow is Temporal-direct spawn by Monitor, NOT scheduled — 2026-07-16)
as their workflows land in O2+.

**Manual trigger** for ad-hoc re-ingest (e.g. testing after a code
change) still works:

```bash
docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/trigger_ingest'
```

This bypasses the schedule entirely and fires a one-off workflow run
against the current worker.

Cross-refs:
- [Plan §10 (deployment)](../rebuild-plan.md#10-deployment) — full deployment spec
- [orchestration.md](./orchestration.md) — workflow inventory + wire-up
- [temporal.md](./temporal.md) — Client/Worker adapter shape
