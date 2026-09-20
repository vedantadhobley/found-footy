# Found Footy operator UI

This directory is a removable, internal-only evidence browser. It is a separate
Go module and container. It imports no Found Footy application package, adds no
public API route, changes no worker contract, and adds no database object. Delete
this directory and its internal Caddy route to remove the feature.

## Boundary

The UI reads the existing contracts directly:

- PostgreSQL supplies fixtures, events, accepted MD5 assets, candidate outcomes,
  validation evidence, shares, supersession edges, and selection receipts.
- Garage supplies retained bytes through a GET-only adapter. Browsers never see
  Garage credentials, internal object keys, or the `garage:3900` hostname.
- The UI exposes its own private JSON and byte-range media routes. These are
  operator routes, not additions to Found Footy's public API.

The PostgreSQL pool sets `default_transaction_read_only=on` for every session.
The code contains no mutating SQL and the Garage adapter exposes only HEAD/GET
behavior. The current deployment reuses the application's Garage key because a
dedicated read-only key has not been provisioned; that is an infrastructure
hardening option, not an application requirement.

## Refresh model

There is no NATS, SSE, watcher, or background-polling dependency. The browser
loads once and refreshes only when the operator presses the top-bar Refresh
button or changes a list filter. Refreshing re-runs the fixture query with the
current search and filter values. It never replaces the selected event panel,
so the chosen asset, playback position, and play/pause state remain intact.
Reselecting an event is the explicit action that reloads its detailed evidence.

## Surfaces

- `GET /` — embedded browser application
- `GET /api/fixtures` — paginated/filterable fixtures with complete nested event membership
- `GET /api/events` — legacy paginated event summaries retained for diagnostics
- `GET /api/events/{event_id}` — all retained event evidence
- `GET /media/{asset_id}` — database-resolved Garage proxy with HTTP Range support
- `GET /healthz` — container readiness

The browser follows a fixture → event → retained-variant hierarchy, matching
the public product's match-level navigation without importing its code or
presentation contract. Fixture pagination never splits one match across pages.
The event detail distinguishes graph-selected roots from clips currently
visible through FF-078 and shows their derived public rank. The module mirrors
the producer's `3`/`1` visibility constants locally to preserve its removable
dependency boundary; a policy change must update both values. It also includes
hidden/shareless variants, superseded variants,
reclaimed SQL evidence, direct support, exact and routed observations, video
metadata, candidate provenance, retained validation, and the latest complete
selection receipt. Rejected candidates remain visible as audit rows, but their staging
bytes were never retained and therefore cannot be played.

The no-store HTML document embeds a content-derived version on its JavaScript
and CSS URLs. Those versioned assets are immutable. A deployment therefore
cannot combine a new DOM contract with a cached bundle from the prior release.

## Deployment shape

The two Compose files are independent of the application stack:

- `compose.prod.yml` joins external networks `found-footy-prod` and `proxy`.
- `compose.dev.yml` joins external networks `found-footy-dev` and `proxy`.

Neither joins `luv-prod`, `luv-dev`, or NATS. Both cap the container at 256 MiB,
run as an unprivileged user on a read-only filesystem, drop Linux capabilities,
and expose no host port.

Compose interpolation must use the parent repository's private environment file
without injecting unrelated variables into the container:

```bash
cd operator-ui
docker compose --env-file ../.env -f compose.prod.yml config
docker compose --env-file ../.env -f compose.prod.yml build
```

Production is deployed at `http://found-footy-prod-ui.<BASE_DOMAIN>`. Its image
uses the source commit as the tag; deploy a new source revision explicitly:

```bash
cd operator-ui
IMAGE_TAG=<source-commit> docker compose --env-file ../.env -f compose.prod.yml build ui
IMAGE_TAG=<source-commit> docker compose --env-file ../.env -f compose.prod.yml up -d --no-deps ui
```

This is a separately approved production action. The companion project must not
be folded into the application deployment command: creating or replacing the UI
must never recreate an API, worker, Twitter, database, Garage, or Temporal
container.

The dev stack is currently down. Its named volumes remain, but its external
`found-footy-dev` network must exist before starting `compose.dev.yml`.

## Development

The nested module has its own container-first gates:

```bash
cd operator-ui
make tidy
make check
make image
```

The UI does not participate in the parent module's dependency graph or tests.
Run both projects' gates when a change touches both surfaces.

## Validation evidence

On 2026-09-19, an explicitly approved temporary container joined the production
project network without a Caddy route. The live schema returned event summaries,
an active-event detail with accepted asset, candidate, validation, and selection
evidence, a Garage request returned a correct `206` 1 KiB byte range, and an SSE
client received natural database-change refreshes. The temporary container was
then removed. The replacement `pg_stat_user_tables` token query was separately
verified read-only against production after the smoke run.

On 2026-09-19, source commit `f558f62` was deployed persistently as
`found-footy-prod-ui` and the workspace Caddy route was hot-reloaded. The root,
health, and event-list routes returned HTTP 200 through
`found-footy-prod-ui.luv`; the container reported healthy with zero restarts, a
read-only root filesystem, and the 256 MiB cap. Existing Found Footy production
containers were not recreated or restarted.

The same day, commit `96cbc1f` corrected DOM event-name normalization and added
detail loading/error states. A disposable Firefox loaded the live internal UI,
clicked an event, and observed the event-detail header and asset projection.
Only `found-footy-prod-ui` was replaced; application containers were unchanged.

On 2026-09-20, commit `1420b93` replaced automatic SSE invalidation with the
manual top-bar refresh contract after live review showed that background detail
replacement interrupted clip comparison. The served content-versioned bundle
contained the manual refresh handler and no `EventSource`; root and health
requests passed. The UI remained healthy with zero restarts, a read-only root
filesystem, and its 256 MiB cap. Found Footy's API, workers, Twitter service,
databases, Garage, and Temporal containers were not recreated.
