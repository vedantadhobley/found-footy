# Found Footy operations

This is the operator runbook for the current Go system. It defines safe
inspection, diagnosis, and recovery boundaries. It does not duplicate the
service topology or workflow design.

## Safety boundary

Production is live. Run a production inspection only when the current task
authorizes that inspection. An inspection never grants permission for an
adjacent mutation.

Every production mutation requires explicit approval for that exact action.
This includes:

- Compose `up`, `down`, `restart`, `stop`, `rm`, build, or recreate operations;
- starting or removing the production VNC container;
- stopping, releasing, reaping, or removing a production Firefox instance;
- Temporal cancel, terminate, reset, retry, or schedule changes;
- non-`SELECT` SQL, direct state repair, and schema changes;
- Garage object changes; and
- edits to production Compose, `.env`, Caddy, or other loaded configuration.

The commands in this document are recipes, not standing authorization. Resolve
the exact target before requesting approval. Do not expose `.env` values,
container environment blocks, cookies, database credentials, or signed object
URLs in logs or reports.

Read-only inspection includes scoped `ps` and logs, service `GET` status
requests, Temporal list/show operations, and SQL `SELECT`. Prefer these before
proposing a recovery action. Avoid broad `docker inspect` output because it can
contain secrets.

## Environment lifecycle

There is deliberately no default Compose file. Always name the environment:

```bash
docker compose -f docker-compose.dev.yml ps
docker compose -f docker-compose.prod.yml ps
```

The routine Makefile lifecycle targets are dev-only:

```bash
make dev-up
make dev-logs
make dev-ps
make dev-down
```

Do not use a dev target as shorthand for production. `make deploy-prod` is the
single production application release target; it is covered by the explicit
approval boundary and is not a general lifecycle command.

The deployed Firefox provisioner scopes names, labels, capacity, release, and
reaping by the Compose-selected network. Dev and production may share the
Docker daemon without acting on each other's browsers. This isolation does not
change the explicit approval boundary for any production mutation. The
[FF-001 release evidence](./history/issue-register-2026-08-17.md#ff-001--firefox-fleet-is-not-environment-scoped)
records the original failure and rollout.

The shared cookie file is `~/.config/found-footy/twitter_cookies.json`. Both
environments use the same Twitter account and cookie channel. Environment
isolation applies to browser ownership and lifecycle, not authentication
identity.

## Routine production inspection

Start with the smallest read-only surface that answers the question:

```bash
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs --since 30m worker twitter api
docker exec found-footy-prod-temporal \
  temporal --address temporal:7233 schedule list
```

Use `logs -f` only while actively watching an incident. Compose service logs
cover both worker replicas without guessing their generated container names.

For an event incident, collect these identifiers before drawing a conclusion:

- fixture ID;
- event UUID and natural key;
- Temporal workflow ID and run ID;
- candidate tweet URL and search attempt;
- asset UUID or share ID, when one exists; and
- timestamps for first observation, failure, and recovery.

The deterministic event workflow ID is `event-<event-uuid>`. Inspect its
history without changing it:

```bash
docker exec found-footy-prod-temporal \
  temporal --address temporal:7233 workflow show \
  --workflow-id 'event-<event-uuid>'
```

Use the Temporal UI when it gives a clearer activity, child-workflow, retry,
or cancellation history. Do not infer workflow success from process liveness
or from the absence of a current log line.

## Database diagnosis

Open a production Postgres session only for an authorized inspection. This
command uses the credentials already present inside the container and does not
print them:

```bash
docker exec -it found-footy-prod-postgres sh -lc \
  'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
```

Keep incident queries read-only. Replace the placeholders below; never paste a
secret or signed URL into the query history.

Fixture state and completion evidence:

```sql
SELECT id, state, api_status_short, home_team_name, away_team_name,
       home_score, away_score, completion_counter,
       last_polled_at, last_activity_at, completed_at
FROM fixtures
WHERE id = <fixture-id>;
```

Detected events and debounce state:

```sql
SELECT id, natural_key, event_type, detail, team_id, team_name,
       player_name, minute, extra, debounce_count,
       downstream_triggered, removed, removed_reason,
       monitor_complete, download_complete, first_seen_at, updated_at
FROM events
WHERE fixture_id = <fixture-id>
ORDER BY minute, extra NULLS FIRST, first_seen_at;
```

Downstream settlement:

```sql
SELECT workflow_type, workflow_id, started_at, completed_at, outcome_class,
       metadata
FROM event_downstream_workflows
WHERE event_id = '<event-uuid>'
ORDER BY started_at;
```

Search attempts and candidate outcomes:

```sql
SELECT search_attempt, tweet_url, outcome_class, reject_reason,
       discovered_at, outcome_at, outcome_detail
FROM event_search_candidates
WHERE event_id = '<event-uuid>'
ORDER BY discovered_at;
```

Promoted assets and public shares:

```sql
SELECT s.id AS share_id, s.state, s.rank, s.timestamp_verified,
       s.extracted_minute, a.id AS asset_id, a.s3_key,
       a.popularity, a.superseded_by
FROM video_shares AS s
JOIN video_assets AS a ON a.id = s.asset_id
WHERE s.event_id = '<event-uuid>'
ORDER BY s.state, s.rank;
```

Interpret these records together:

- A fixture is not settled merely because API status is terminal. Its event
  inventory and downstream checklist must also agree. FF-014's deployed
  score-consistency gate withholds terminal votes until the reported score and
  stored goal inventory match.
- `completed_at IS NULL` in `event_downstream_workflows` means the fixture is
  still waiting on that workflow. It can also indicate abnormal workflow
  closure; see FF-007 and FF-015 before attempting recovery. A still-running
  EventWorkflow is recovered automatically only after FF-025 observes the same
  run with no Temporal counter or heartbeat progress for its full conservative
  window. Do not terminate it or stamp the checklist by hand based on age.
- Candidate outcomes are `promoted`, `duplicate`, `superseded`, `rejected`,
  `failed`, or `pending`. A parent EventWorkflow that has completed while one
  of its candidates remains `pending` is not normal propagation delay. Capture
  the workflow and candidate evidence as a regression of the deployed FF-002
  terminal-outcome contract. Do not stamp the row by hand.
- Zero promoted clips after all configured attempts can be legitimate. Confirm
  the search count and terminal candidate reasons before classifying it as a
  discovery failure.

## Firefox fleet diagnosis

Fleet containers are daemon-global resources. The scoped implementation labels
each instance with:

- `found-footy.fleet=firefox`;
- `found-footy.fleet.scope=<compose-network>`; and
- `found-footy.fleet.event=<event-uuid>`.

Its daemon name is
`<scope>-firefox-ev-<event-uuid>`. The network-local alias remains
`ff-firefox-ev-<first-eight-event-uuid-characters>` for Temporal history
compatibility.

List the production scope without mutating it:

```bash
docker ps -a \
  --filter 'label=found-footy.fleet=firefox' \
  --filter 'label=found-footy.fleet.scope=found-footy-prod' \
  --format 'table {{.Names}}\t{{.Status}}\t{{.Label "found-footy.fleet.event"}}'
```

For every unexpected container, correlate its event ID with the event row,
downstream checklist, and Temporal execution. A stopped container can still
consume fleet capacity until it is reaped. The staging poll reaper is the
normal crash backstop.

Do not manually remove an instance because its parent workflow appears closed.
Correlate the workflow, downstream row, event, and ownership labels first; a
closed historical execution can still coexist with current recovery state.
Production workers are scope-aware, but that ownership proof does not authorize
manual deletion. If cleanup is required, list the exact container names and
request approval for those removals only.

## Twitter authentication and cookie re-auth

`GET /status` is the read-only source for browser state, reason, busy flag,
cookie fingerprint, last authentication check, and last loaded cookie mtime.
`GET /authenticate` reports whether manual re-auth is required. `POST
/auth/verify` is intentionally stateful: it forces session verification and
writes a successful cookie refresh.

The repository's production `/authenticate.reauth_command` explicitly names
`docker-compose.prod.yml` (FF-018), and the deployed service advertises that
explicit command. Starting or removing the VNC service remains a production
mutation that requires separate approval.

Production re-auth procedure:

1. Confirm `unauthenticated` from the production Twitter service and record the
   reason. A `failed` browser is a different incident; re-auth is not its
   automatic remedy.
2. Request approval to start the production VNC service.
3. After approval, run:

   ```bash
   docker compose -f docker-compose.prod.yml --profile vnc \
     up -d --build twitter-vnc
   ```

4. Open `http://found-footy-prod-twitter-vnc.luv` and log in to X.
5. From an already authorized diagnostic client on the production network,
   send `POST http://found-footy-prod-twitter-vnc:8888/auth/verify`. Require a
   `200` response with `{"state":"healthy"}`. Do not substitute a `GET`.
6. Compare `/status` before and after. A new `cookie_fingerprint` or
   `last_loaded_mtime`, followed by a healthy search instance on its next
   authentication check, proves propagation.
7. Request separate approval to stop and remove the production VNC service.
8. After approval, run:

   ```bash
   docker compose -f docker-compose.prod.yml --profile vnc stop twitter-vnc
   docker compose -f docker-compose.prod.yml --profile vnc rm -f twitter-vnc
   ```

`make twitter-vnc-up`, `make twitter-vnc-down`, and
`make twitter-vnc-logs` operate on dev only. Never use them for production
reauth.

## Recovery boundaries

Prefer deterministic self-recovery over an ad-hoc write:

| Symptom | First action | Do not do |
|---|---|---|
| One active-poll API chunk failed | Let the next scheduled cycle request the missed fixture IDs; inspect again only if that cycle also fails. | Trigger duplicate monitor work immediately. |
| Schedule timing differs from config | Describe the stored Temporal schedule and compare it with config; track under FF-009. | Assume worker restart reconciles an existing schedule. |
| Parent complete, candidate `pending` | Preserve workflow history and candidate evidence; handle under FF-002. | `UPDATE` the candidate outcome manually. |
| Workflow canceled or failed with open checklist row | Correlate Temporal history, checklist, and fleet state; handle under FF-007 or FF-015. | Reset, retry, cancel, or close the row without an issue-specific recovery plan. |
| Terminal score exceeds surviving goal inventory | Keep the fixture/event evidence together; handle under FF-014. | Mark the fixture complete or classify the absent goal as VAR by hand. |
| Firefox container stopped or orphaned | Correlate its scoped labels with Postgres and Temporal, then let the reaper act. | Remove it based only on container state. |
| Twitter `unauthenticated` | Use the VNC procedure above. | Restart the entire stack or delete the cookie file. |
| `twitter.browser_failed` or event-browser restart | Confirm the same container returns healthy before the next Temporal retry; inspect repeated exits for memory pressure or corrupt profile state. | Treat HTTP process liveness alone as browser health, or manually remove an event browser that Docker is recovering. |

The scheduled workflows use Temporal's `SKIP` overlap policy, so a slow cycle
does not create a concurrent copy. Schedule registration is create-only.
`ActivePollWorkflow` and API fixture-ID chunks run concurrently within worker
caps. A total chunk failure leaves that cycle unchanged; the next cycle is the
first recovery mechanism.

`EventWorkflow` is durable Temporal work. New executions run download and hash
activities directly around the exact-MD5 claim; pre-FF-022 histories can still
contain the registered `VideoWorkflow` child. Cancellation stops downstream
work. Normal finalization closes the Postgres checklist and releases the
event's Firefox instance; the staging-poll reaper handles a worker crash or
failed release. The deployed recovery contracts and their original violations
are preserved under
[`FF-002`](./history/issue-register-2026-08-17.md#ff-002--failed-video-child-leaves-candidate-pending),
[`FF-007`](./history/issue-register-2026-08-17.md#ff-007--abnormal-eventworkflow-closure-can-strand-a-fixture),
and [`FF-015`](./history/issue-register-2026-08-17.md#ff-015--canceled-eventworkflow-spins-into-temporal-deadlock-detection).

The ingest team cache is replaced transactionally only after at least one
configured league refreshes. Failed or empty leagues retain their prior rows
for the next daily retry. If the cache is empty, date-based fixture ingest
fails closed. Do not bypass that guard with a broad vendor import.

Manual trigger programs under `scripts/` are development and diagnosis tools.
Some insert checklist rows or start deterministic workflow IDs. Do not point
them at production as a recovery shortcut.

## Production rollout and rollback gates

A production rollout is a sequence of separately authorized mutations, not an
extension of a read-only investigation. Before requesting rollout approval:

1. identify the exact commit and affected services;
2. run the relevant unit, integration, and scenario tests;
3. validate the production Compose model without publishing its resolved
   environment;
4. state schema, object-storage, Temporal-history, and Firefox-fleet
   compatibility;
5. define observable success and failure markers; and
6. define a rollback that preserves Postgres, Garage, and the archived Python
   rollback volumes.

The approved application rollout is `make deploy-prod`. It builds the exact
clean checked-out commit, selects active production event browsers by their
fleet label plus production-network membership, and checks for them both before
the build and immediately before mutation. It runs the worker permission smoke,
recreates only the application services, and verifies the expected identity on
every recreated process. It does not update source, change schema, restart
infrastructure, clean fleet containers, or roll back on failure. See the
[deployment contract](./deployment.md#deploy-tracking).

If a legacy unscoped `ff-firefox-ev-*` container appears, stop the rollout and
identify its workflow and network ownership. The scoped provisioner cannot
safely adopt or remove it. Legacy cleanup and the production rollout require
separate explicit approvals.

After an approved rollout, verify the built commit identity, worker
registration, stored schedules, browser scope labels, API health, and one
end-to-end event path. Do not declare success from container uptime alone.

If rollback becomes necessary, stop and construct it from the observed failure
and the exact deployed state. Never drop or recreate durable volumes as part of
a routine rollback.

## Authority map

- [`deployment.md`](./deployment.md) owns Compose topology, bootstrap, and
  routing.
- [`api.md`](./api.md) owns the current HTTP and NATS live-feed consumer contracts.
- [`twitter-service.md`](./twitter-service.md) owns the scraper HTTP contract,
  browser state machine, and cookie model.
- [`temporal.md`](./temporal.md) and
  [`orchestration.md`](./orchestration.md) own workflow registration, retry,
  and lifecycle contracts.
- [`observability.md`](./observability.md) and
  [`logging.md`](./logging.md) own metrics, vocabulary, and log emission.
- [`todo.md`](./todo.md) owns known bugs, recovery hazards, and operational
  holds.
