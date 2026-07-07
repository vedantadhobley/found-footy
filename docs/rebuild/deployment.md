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

- `docker-compose.dev.yml` — hot-reload dev stack. All four Go binaries
  (worker, api, scaler, twitter) run via `air` with source bind-mounted.
- `docker-compose.prod.yml` — currently still runs the Python codebase.
  Will migrate to Go binaries during Phase M/C cutover per rebuild-plan.md
  §13.

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

## Workflow scheduling — NOT WIRED

Plan §5 W1 says IngestWorkflow runs on `5 0 * * *` (daily 00:05 UTC)
via a Temporal Schedule registered at worker startup. **No schedules
are registered in `cmd/worker/main.go` today.** The workflow is
registered on the worker (per O1d) but only fires from a manual
trigger:

```bash
docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/trigger_ingest'
```

Schedule registration is an O1e task before MonitorWorkflow starts.
The Python-era `archive/src/worker.py` shows the reference pattern
(client.create_schedule with ScheduleSpec + Schedule); Go SDK
equivalent is `client.Schedule()` + `client.ScheduleClient()`.

Cross-refs:
- [Plan §10 (deployment)](../rebuild-plan.md#10-deployment) — full deployment spec
- [orchestration.md](./orchestration.md) — workflow inventory + wire-up
- [temporal.md](./temporal.md) — Client/Worker adapter shape
