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

For a match-day overview, use the checked-in read-only report:

```bash
scripts/matchday-status.sh prod 6
scripts/matchday-status.sh dev 6
```

The second argument is the kickoff lookahead in hours, from 0 through 72. The
report derives the Compose file, Postgres container, and Firefox ownership
scope from `dev` or `prod`; it does not read or print dotenv values. Its SQL
runs inside `BEGIN READ ONLY` with a statement timeout. It shows service and
scoped fleet state, recent/upcoming or active fixtures, event/downstream/
candidate/share progress, and the count plus ten newest FF-034 durability
violations. For the same fixture window it groups terminal FF-060 download
failures by bounded stage/class, retaining `legacy_unclassified` for histories
that predate the versioned payload. It performs no Temporal describe calls; use
the event workflow ID from the report for deeper inspection.

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
       home_score, away_score, terminal_observed_at,
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
       outcome_detail#>>'{failure,stage}' AS failure_stage,
       outcome_detail#>>'{failure,class}' AS failure_class,
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

- A live-polled fixture is not settled merely because API status is terminal.
  `terminal_observed_at` starts its configured grace period. Completion remains
  blocked while a named event is debouncing or any downstream checklist is
  open. Provider and stored-score parity are completion audit evidence, not a
  completion gate; score still protects a required goal from false VAR
  removal. Historical terminal ingests retain their direct-complete path.
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
- For FF-061 histories, `metadata.attempts_completed` counts usable rendered or
  explicit-empty observations. `metadata.unavailable_attempts` counts bounded
  probes that did not reduce that budget. Inspect `last_search_state` and
  `last_search_evidence` before attributing an outage to auth, rate limiting,
  or X downtime. The evidence intentionally contains no response body or
  credentials.

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
cookie fingerprint, last authentication check, last loaded cookie mtime, and
nested cookie backup/reload attempt-success-error evidence.
`GET /authenticate` reports whether manual re-auth is required. `POST
/auth/verify` is intentionally stateful: it forces session verification and
writes a successful cookie refresh.

`twitter-maintenance-scheduled` runs against the static service at minute 17
every six hours by default. It forces verification and cookie writeback, then
requires live-search article, video, and status-link evidence. Inspect its most
recent Temporal execution when an event browser reports auth or DOM trouble.
This schedule preserves and diagnoses an existing session even during a week
without events; it cannot create new credentials after full expiry.

The opt-in VNC container runs raw Firefox ESR with no Playwright or WebDriver.
After the operator closes Firefox, its capture service reads the profile's
native cookie file through SQLite, requires a
non-expired `auth_token`, and atomically writes the browser-neutral backup. Its
read-only `/status` must reach `state=ready`; merely seeing a logged-in page is
not proof. A `degraded` search state means verification was inconclusive, not
that credentials are proven expired. A `failed` search browser is a separate
lifecycle incident.

Dev recovery uses `make twitter-vnc-up`, login through
`http://found-footy-dev-twitter-vnc.luv`, capture-status inspection, a forced
POST to the static service's `/auth/verify`, then `make twitter-vnc-down`.

Production recovery remains several separately authorized mutations:

1. Read the static service status, the last maintenance execution, and the
   existing cookie fingerprint. Confirm `unauthenticated`, not merely
   `degraded`.
2. Request approval to start `found-footy-prod-twitter-vnc`, then run the exact
   Compose command returned by `/authenticate`.
3. Log in through `http://found-footy-prod-twitter-vnc.luv`, then close Firefox
   normally to release its cookie-database lock. The capture service and noVNC
   remain running.
4. Read the auth container's internal `:8888/status` from an authorized
   diagnostic path. Require `state=ready`, a post-login `last_capture`, a future
   `auth_expires_at`, and a non-empty fingerprint. Never print cookie values.
5. Request separate approval to POST the production static service's
   `/auth/verify`. Require HTTP 200 and a current static backup success.
6. Let the next natural event prove a fresh event browser reloads and searches;
   do not create production discovery work only as a probe.
7. Request separate approval to stop and remove the VNC container.

Do not delete the cookie file, repeatedly restart the fleet, copy the Firefox
profile into a search container, or treat raw capture without static
verification as complete recovery.

## Historical candidate replay

`scripts/replay_clock_rejects` is the narrow FF-057 repair runner. It does not
reset a completed Temporal history, discover new tweets, or directly create an
asset. It selects only terminal candidates with the exact clock-mismatch
reason, preserves their prior verdict, and executes a new deterministic
EventWorkflow identity through the normal download, validation, deduplication,
ranking, publication, and cleanup path.

The runner requires `FIXTURE_ID` and `EXPECTED_EVENT_COUNT`, limits each event
to 50 selected candidates by default, and is dry-run unless
`REPLAY_APPLY=true`. Its Postgres transaction commits before the Temporal
start. If the process stops in that gap, rerunning the same command finds the
existing checklist and resumes the same failed-only identity without resetting
candidate rows again. It also normalizes the exact two-element JSON array shape
produced by the initial FF-057 replay when a duplicate terminal outcome carried
a JSON `null` detail; no other candidate detail is rewritten. Events run
sequentially. A successful command verifies
the checklist, selected count, and zero pending replay rows after each event.

Build the runner from the exact reviewed checkout with the pinned toolchain.
Building is not a production mutation:

```bash
mkdir -p /tmp/found-footy-replay
docker run --rm \
  -e CGO_ENABLED=0 -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD":/src:ro -v /tmp/found-footy-replay:/out \
  -v "$HOME/.cache/found-footy/gocache":/gocache \
  -v "$HOME/.cache/found-footy/gomodcache":/gomodcache \
  -w /src golang:1.25.11-bookworm \
  go build -buildvcs=false -o /out/replay-clock-rejects \
  ./scripts/replay_clock_rejects
```

Plan against production without mutation. Replace both placeholders with the
reviewed values:

```bash
docker run --rm --network found-footy-prod --env-file .env \
  -e FIXTURE_ID=<fixture-id> -e EXPECTED_EVENT_COUNT=<count> \
  -v /tmp/found-footy-replay/replay-clock-rejects:/replay:ro \
  golang:1.25.11-bookworm /replay
```

The plan must print the expected event identities and exact candidate count.
Stop if any event is absent, has zero selected candidates, or exceeds the
ceiling. Deploy the evaluator fix before applying its historical repair.
Applying the plan performs Postgres DML and starts Temporal workflows, so it
requires separate explicit production approval:

```bash
docker run --rm --network found-footy-prod --env-file .env \
  -e FIXTURE_ID=<fixture-id> -e EXPECTED_EVENT_COUNT=<count> \
  -e REPLAY_APPLY=true \
  -v /tmp/found-footy-replay/replay-clock-rejects:/replay:ro \
  golang:1.25.11-bookworm /replay
```

Keep the command output with the incident. The prior and replacement candidate
evidence remains in `event_search_candidates.outcome_detail.replay`. Existing
active shares stay live during replay. See the
[historical replay decision](./decisions/2026-08-19-historical-candidate-repair-reuses-event-workflow.md).

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
| Twitter `degraded` | Inspect the last maintenance run and `/status` cookie error evidence; distinguish network/DOM failure from auth expiry. | Declare the account unauthenticated without a login redirect. |
| Twitter `unknown_timeout` / historical `feed_timeout` burst | Inspect downstream `last_search_evidence` and bounded result-state metrics across unrelated events; let the unavailable budget retry at one-minute cadence. | Call it an empty query, re-authenticate without a login state, increase the ten-second bound, or load-test the production account. |
| Twitter `unauthenticated` | Use the raw-Firefox capture and static-verify procedure above; production mutations each need approval. | Restart the entire stack, delete the cookie file, or copy a Firefox profile into search. |
| `twitter.browser_failed` or event-browser restart | Confirm the same container returns healthy before the next Temporal retry; inspect repeated exits for memory pressure or corrupt profile state. | Treat HTTP process liveness alone as browser health, or manually remove an event browser that Docker is recovering. |

The four scheduled workflows use Temporal's `SKIP` overlap policy, so a slow cycle
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

Manual trigger programs under `scripts/` are development and diagnosis tools
unless this runbook names a specific guarded operator procedure. Some insert
checklist rows or start deterministic workflow IDs. Do not point them at
production as an unreviewed recovery shortcut.

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

FF-013 preserves the two-action production boundary in code. `make
migrate-prod` builds the exact clean commit and runs its dedicated migration
binary on the production network. It applies the ordered checksummed chain in
one transaction and prints `migration verified: <sha>` only after the ledger,
required objects, and schema stamp commit. This is a production database
mutation and requires its own explicit approval. `make deploy-prod` remains a
separate approval; worker/API only verify the already-applied chain. If no
migration is pending, the explicit command is idempotent.

Constraint migrations are deliberately fail-closed. A preflight error means
the existing rows violate the new invariant; the transaction has not applied
DDL or recorded a ledger row. Inspect and repair the reported class under a
separate approved database action, then rerun the same immutable migration.

Rollback must account for the migration ledger as well as SQL compatibility.
An older binary whose embedded chain does not contain a live ledger row fails
closed. Do not delete or rewrite ledger rows to force it through; inspect the
exact change and choose a compatible image or a reviewed forward repair.

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
  the [`orchestration` ledger](./orchestration/) own workflow registration, retry,
  and lifecycle contracts.
- [`observability.md`](./observability.md) and
  [`logging.md`](./logging.md) own metrics, vocabulary, and log emission.
- [`todo.md`](./todo.md) owns known bugs, recovery hazards, and operational
  holds.
