# Operations

Runbook for bringing the stack up, re-authing Twitter, scaling, and triaging
common failure modes.

## Prereqs

- Docker + Docker Compose
- Shared docker networks exist (one-time, idempotent):
  ```bash
  docker network create luv-prod
  docker network create luv-dev
  docker network create proxy   # if not already created by the workspace proxy stack
  ```
- The workspace `proxy` Caddy is running with `~/workspace/proxy/caddy/caddy.d/found-footy.caddy` populated. See `deploy/INFRA-NOTES.md`.
- `.env` filled out from `.env.example`.

## Bring-up

```bash
cd ~/workspace/dev/found-footy
cp .env.example .env
$EDITOR .env                                                   # API_FOOTBALL_KEY, TWITTER_*, LLAMA_URL

docker compose -f docker-compose.dev.yml up -d --build         # dev
# or
docker compose -f docker-compose.yml up -d --build             # prod
```

In prod, the scaler service starts immediately and brings up the minimum 2
worker + 2 twitter replicas via `docker compose --scale`. The `worker` and
`twitter` services run as `replicas: 2` by default but the scaler can grow
them up to 8.

## URLs (replace `<base-domain>` with `$BASE_DOMAIN`)

| Service | URL |
|---|---|
| Temporal UI | `http://found-footy-{dev,prod}-temporal-ui.<base-domain>` |
| MongoDB UI (dev) | `http://found-footy-dev-mongoku.<base-domain>` |
| MongoDB UI (prod) | `http://found-footy-prod-mongo-express.<base-domain>` |
| MinIO console | `http://found-footy-{dev,prod}-minio.<base-domain>` |
| Twitter VNC (dev) | `http://found-footy-dev-twitter.<base-domain>` (always on) |
| Twitter VNC (prod) | `http://found-footy-prod-twitter-vnc.<base-domain>` (only when `vnc` profile is up) |

Verify after bring-up:
```bash
curl -sI http://found-footy-prod-temporal-ui.${BASE_DOMAIN:-luv}/
curl -sI http://found-footy-prod-mongo-express.${BASE_DOMAIN:-luv}/
curl -sI http://found-footy-prod-minio.${BASE_DOMAIN:-luv}/
```

## Twitter cookie re-auth

When the Twitter service starts returning empty search results, cookies have
likely expired (re-auth is needed every few weeks).

### Prod (manual `vnc` profile)

```bash
docker compose up -d twitter-vnc                  # spins up the vnc-enabled twitter container
# Open http://found-footy-prod-twitter-vnc.<base-domain>/ — log into Twitter in the Firefox window
docker compose stop twitter-vnc                   # cookies are persisted to the volume + host config
```

### Dev (VNC is always on)

Just open `http://found-footy-dev-twitter.<base-domain>/` and log in.

Cookies persist to the `found-footy-{dev,prod}-twitter-cookies` docker
volume + a backup at `~/.config/found-footy/twitter_cookies.json`.

## Scaling

The scaler service is always running. It checks every 30 s with a 60 s
cooldown between scaling actions:

- **Workers** scale on Temporal task-queue depth (scale up when > 5 pending tasks/worker, down when < 2).
- **Twitter** instances scale on the count of active goals in MongoDB (goals with `_monitor_complete=true` and `_download_complete=false`).

Both pools clamp to `[MIN_INSTANCES=2, MAX_INSTANCES=8]`.

Manual override (bypasses the scaler temporarily until its next tick):
```bash
docker compose up -d --scale worker=4 --scale twitter=4
```

Scaler decisions are visible in the scaler logs:
```bash
docker compose logs -f scaler
```

## LLM health check

```bash
curl http://llama-small.joi/v1/models | jq '.data[].id'
curl http://llama-small.joi/health
```

If the model is loading or unreachable, AI validation activities will time
out and `_download_complete` will stay `false` indefinitely. The 2-concurrent
LLM cap is enforced JVM-side on joi; this project must not exceed it.

## Common issues

| Symptom | Likely cause | Where to look |
|---|---|---|
| Fixture stuck `active`, events stuck "extracting" forever | Twitter workflow re-firing or download workflows registered but stuck mid-AI-validation | Temporal UI — filter by `twitter-*-{event_id}` and `download*-*-{event_id}`; check status counts |
| Twitter search returns empty | Cookies expired | "Twitter cookie re-auth" above |
| Videos not uploading | MinIO/S3 unreachable from worker | `docker compose logs worker` for s3 errors |
| Same goal has duplicate S3 videos | Dedup `break` bug | `docs/proposals/dedup-unification.md` |
| AI validation timing out | joi llama-small unreachable, model still loading, or > 2 concurrent calls | `curl $LLAMA_URL/v1/models` |
| Scaler logs spam "active goals X, twitter pool Y" without scaling | Last scaling action within the 60 s cooldown | Normal; wait |

## Inspecting state

### MongoDB

```bash
# Shell into prod mongo
docker exec -it found-footy-prod-mongo \
  mongosh -u ffuser -p ffpass --authenticationDatabase admin found_footy

# Find an event
db.fixtures_active.aggregate([
  {$unwind: "$events"},
  {$match: {"events._event_id": "<event_id>"}}
])

# Stuck "extracting" events
db.fixtures_active.aggregate([
  {$unwind: "$events"},
  {$match: {
    "events._monitor_complete": true,
    "events._download_complete": false
  }},
  {$project: {"events._event_id": 1, "events._first_seen": 1, "events._download_workflows": 1}}
])
```

### S3

```bash
docker exec found-footy-prod-worker python -c "
from src.data.s3_store import FootyS3Store
s3 = FootyS3Store()
for obj in s3.s3_client.list_objects_v2(Bucket='footy-videos').get('Contents', [])[:20]:
    print(obj['Key'], obj['Size'])
"
```

## Tests

```bash
# Inside the dev worker container
docker exec found-footy-dev-worker python -m pytest tests/ -v
```

The two scripts at `scripts/test_clock_extraction.py` and
`scripts/test_structured_extraction.py` are **not** pytest tests — they're
batch harnesses for iterating on the AI vision prompt. Run them ad-hoc
when changing the prompt or the clock-extraction parsers.

## Bring-down

```bash
docker compose -f docker-compose.dev.yml down                  # dev
docker compose -f docker-compose.yml down                      # prod (keeps volumes)
```

Volumes (`found-footy-{dev,prod}-mongo-data`, `-postgres-data`, `-minio-data`,
`-twitter-cookies`, `-temp`) survive. To wipe a stack, add `-v`.
