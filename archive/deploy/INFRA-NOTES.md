# Deploy notes — what changes outside this repo

The application code is fully contained in this repo. The host-level
infrastructure changes needed to run found-footy on luv live in the
workspace's `~/workspace/proxy/` stack — they aren't tracked here because
they're cross-cutting, but they're listed here so the deploy is
reproducible from one place.

found-footy has **no public-facing component** (no Cloudflare tunnel
ingress); the workers and twitter scraper run headless and write to the
internal databases. Admin UIs are tailnet-only.

## 1. Caddy routes (luv)

The Caddy config on luv is split per-project. This project's routes live in:

```
~/workspace/proxy/caddy/caddy.d/found-footy.caddy
```

Reference content (current source of truth is the file above):

```caddy
# ─── prod ──────────────────────────────────────────────────────────────────
http://found-footy-prod-temporal-ui.{$BASE_DOMAIN}    { reverse_proxy found-footy-prod-temporal-ui:8080 }
http://found-footy-prod-mongo-express.{$BASE_DOMAIN}  { reverse_proxy found-footy-prod-mongo-express:8081 }
http://found-footy-prod-minio.{$BASE_DOMAIN}          { reverse_proxy found-footy-prod-minio:9001 }
# noVNC inside the manually-started cookie-reauth container (vnc profile)
http://found-footy-prod-twitter-vnc.{$BASE_DOMAIN}    { reverse_proxy found-footy-prod-twitter-vnc:6080 }

# ─── dev ───────────────────────────────────────────────────────────────────
http://found-footy-dev-temporal-ui.{$BASE_DOMAIN}     { reverse_proxy found-footy-dev-temporal-ui:8080 }
http://found-footy-dev-mongoku.{$BASE_DOMAIN}         { reverse_proxy found-footy-dev-mongoku:3100 }
http://found-footy-dev-minio.{$BASE_DOMAIN}           { reverse_proxy found-footy-dev-minio:9001 }
# noVNC inside the dev twitter container (always running in dev)
http://found-footy-dev-twitter.{$BASE_DOMAIN}         { reverse_proxy found-footy-dev-twitter:6080 }
```

After editing the file, reload Caddy without restarting the container:

```bash
docker exec proxy-caddy caddy reload --config /etc/caddy/Caddyfile
```

(The proxy stack uses a directory bind mount, so atomic-write edits to
files inside `caddy/` flow through and `caddy reload` picks them up.
See `~/workspace/proxy/README.md` for the gotcha mechanics.)

## 2. Non-HTTP host ports kept

found-footy still publishes one non-HTTP host port intentionally:

| Container | Port | Why |
|---|---|---|
| `found-footy-dev-temporal` | `7233` (gRPC) | for host-side dev clients connecting to dev temporal directly |

Prod temporal does not publish 7233 (no host clients in prod).

## 3. Cross-project network dependency

found-footy's workers and api connect to `vedanta-systems-prod-api:3001`
over the shared `luv-prod` docker network (and similarly for dev on
`luv-dev`). That network must exist before the stack comes up:

```bash
docker network create luv-prod
docker network create luv-dev
```

(One-time on a fresh node; idempotent if already created.)

## 4. Bring up

```bash
cd ~/workspace/dev/found-footy
cp .env.example .env
$EDITOR .env                                  # set the secrets, including LLAMA_URL
docker compose -f docker-compose.yml up -d --build         # prod
# or
docker compose -f docker-compose.dev.yml up -d --build     # dev
```

## 5. Verify

```bash
curl -sI http://found-footy-prod-temporal-ui.luv/
curl -sI http://found-footy-prod-mongo-express.luv/
curl -sI http://found-footy-prod-minio.luv/
# Twitter VNC only when the `vnc` profile is active (manual cookie reauth)
```
