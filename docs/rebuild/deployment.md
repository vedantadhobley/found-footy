# deployment.md — Go rebuild

**Phase F stub.** Compose file reference + Caddy routing catalog.

Target content:

- docker-compose.yml (prod) + docker-compose.dev.yml (dev) reference:
  service inventory, dependencies, network attachments, volume mounts
- Caddy fragment at `caddy/found-footy.caddy` — hostname → target
  reverse-proxy map. Currently central at
  `~/workspace/proxy/caddy/caddy.d/found-footy.caddy`; migration to
  glob-import deferred per workspace TODO
- Cross-project network setup (`luv-prod`, `luv-dev`, `proxy` — external
  networks created once at workspace setup)
- Workspace-shared dependencies: `~/workspace/nats/`,
  `~/workspace/proxy/`, `~/workspace/monitor/`
- Host paths + data volumes: `~/workspace/data/found-footy/*`
- Deploy tracking: ldflags-baked git SHA + built_at visible in
  `/metrics` + startup log line

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§10 (deployment).
