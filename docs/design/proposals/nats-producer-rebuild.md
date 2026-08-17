# NATS producer rebuild — the 3-subject live-feed model

> **Shipped execution spec; retained as historical rationale.** The live
> consumer contract is [`../../api.md`](../../api.md)
> plus the schemas in the workspace NATS repository. Use the as-built code and
> those schemas when this document differs.

Rip the 6 transition-subjects out of the eventing layer and replace them with the
consumer-driven model settled 2026-08-14: **two batch fixture subjects + one
async event subject**, wrapped in the standard envelope, validated against the
schemas in `~/workspace/nats/schemas/`; cross-project ownership is recorded in
the [dhobley messaging topology](../../../../../vedanta-dhobley/docs/topology.md#messaging--events).
This doc is
the execution spec — read it, then build it in one pass.

Context: the contract is drafted + validated in the `nats/` repo. The API already
fits (bulk `/fixtures?ids=` returns full DTOs *with* events; single `/events/{id}`).
This is **not a cutover blocker** — the frontend renders on plain REST polling
without any of this (design.md "REST is truth, SSE is a hint"). It's a live-feel
enhancement; dev-MVP is the Coppa Italia test, prod hardening is the fast-follow.

## The three subjects

| Subject | Producer(s) | Fires when | Payload | Consumer |
|---|---|---|---|---|
| `found-footy.fixture.clock` | monitor | match minute advanced **only** (frozen clock ⇒ silent) | inline `[{fixture_id, minute, extra}]` | tick — no fetch |
| `found-footy.fixture.update` | **ingest + monitor** | new/refreshed fixture (ingest); kickoff / new-or-removed event / FT / score / pen / winner (monitor) | `{fixture_ids}` | bulk-fetch `/fixtures?ids=` |
| `found-footy.event.video` | downstream (persist/rank) | one event's clip set/rank changed | `{event_id, fixture_id}` | fetch `/events/{id}` |

Per monitor cycle, `clock` and `update` are **disjoint** — a structural fixture
goes in `update` only (its fresh clock rides the full fetch). Envelope:
`{id, ts, source, version, subject, payload}` — `source` = `found-footy-dev`/`-prod`.

## API surface — how consumers read (2 data endpoints + a redirect)

The live feed is only ever deltas on top of a REST snapshot. The read API already
has every capability the model needs — the work is **consolidation**, aligning the
surface with exactly the access patterns:

- `GET /fixtures` — **no params = the full window** (staging + active + recent-
  completed, each with events + videos): serves both the **initial page load** and
  the **reconnect snapshot** (same call). **`?ids=` = the subset** for a
  `fixture.update` batch. One endpoint, both jobs.
- `GET /events?ids=` — bulk (1-or-N) event fetch for `event.video` (coalesce-
  friendly; a single is just `?ids=X`).
- `GET /videos/{share_id}` — 302 → presigned clip URL. Playback, not data.

**Consolidation:** drop the redundant single-resource `GET /fixtures/{id}` and
`GET /events/{id}` — the window/bulk forms cover them and the BFF never fetches a
lone resource. No new endpoint is added; two are removed.

### Initial load + reconnect are the same call

Both are `GET /fixtures` (window). The window read fans out `loadState(staging,
no-events)` + `loadState(active, +events)` + `loadState(completed-recent, +events)`,
loading each fixture's events + videos + phase. On (re)connect the client
re-snapshots the window, renders it, then resumes applying live deltas on top.
Gap-dropped messages are moot — the snapshot already reflects current DB truth, so
you **re-snapshot, not replay**. Every apply is idempotent (refetch/tick), so a
stale delta re-applied right after the snapshot is harmless — no ordering guarantee
needed between snapshot and resumed deltas. (Concrete form of gap-item #1.)

## Architecture — the decoupling

Today one `Composer.Publish` = one `event_log` row **+** one NATS message, per
transition. The rebuild **splits those two planes**:

- **`event_log` (Postgres) = the audit plane.** Stays exactly as-is: per-transition
  rows, semantic `event_type` (detected/stable/removed/…). The Composer keeps
  writing it; we only remove its NATS side. This is why coarsening NATS costs
  nothing in observability — the fine grain lives here.
- **NATS = the live-fanout plane.** A new `NatsPublisher` emits the 3 subjects.
  The `Kind` enum stops being double-duty (subject ⇄ event_type); `event_log`
  keeps its semantic types, NATS gets the 3 subjects.

Emit points:
- **`ActivePollWorkflow`** (end of cycle) → partition its reconcile results →
  `PublishFixtureBatch(clock[], update[])` activity → `fixture.clock` +
  `fixture.update`.
- **`IngestWorkflow`** (end of upsert) → `fixture.update` for *changed* fixtures.
- **persist/rank activity** → `event.video` when clips land / rank shifts.

Workflows can't do NATS I/O directly → all emits go through activities.

## Classification — clock vs update vs nothing

Per fixture, per cycle:
- **structural** (→ `fixture.update`): new event, removed event, state transition
  (staging→active / active→completed), score change, penalty change, winner change,
  status change (e.g. 1H→HT). **Or, ingest: row is new or a field differs.**
- **clock-only** (→ `fixture.clock`): `minute`/`extra` advanced and nothing above.
- **nothing** (→ no message): identical to last poll (stalled minute, frozen HT).

The gate needs signals neither reconcile reports today:
- `ReconcileFixtureOutput` has the structural flags but **not `minute` nor
  "minute changed"** → add them.
- Ingest's `CategorizeAndUpsertFixtures` **upserts unconditionally** — no
  new/modified/unchanged signal → add a change-detect (compare to prior row, or
  `RETURNING` insert-vs-update + a field diff).

## Touch points (file-by-file)

1. `internal/infra/event/subjects.go` — replace 6 Kinds with the 3 subjects + the
   `found-footy.` prefix.
2. `internal/infra/event/payloads.go` — replace payload structs with the 3 that
   match the JSON schemas (clock batch, update batch, event.video).
3. `internal/infra/event/envelope.go` (new) — the standard envelope + `source`.
4. `internal/infra/event/publisher.go` (new) — `NatsPublisher`: `PublishFixtureClock`,
   `PublishFixtureUpdate`, `PublishEventVideo`; marshals envelope, validates in
   tests against the goldens.
5. `internal/infra/event/composer.go` — drop the NATS publish; keep `event_log`.
6. `internal/activity/monitor/activities.go` — `ReconcileFixtureOutput` gains
   `Minute`, `Extra`, `MinuteChanged`, `Structural`; reconcile sets them; rip the
   old per-transition `Composer.Publish` emits (detected/stable/removed).
7. `internal/activity/monitor/publish.go` (new) — `PublishFixtureBatch` activity.
8. `internal/workflow/active_poll.go` — after the reconcile loop, partition +
   call `PublishFixtureBatch`.
9. `internal/activity/ingest/activities.go` — change-detect in the upsert path +
   collect changed ids.
10. `internal/workflow/ingest.go` — call `PublishFixtureBatch` (update only) with
    the changed ids.
11. `internal/activity/video/persist.go` — emit `event.video` on promote/supersede/
    rank change.
12. `internal/config/` — a `source`/env-identity field for the envelope.
13. Metrics: the nats instruments' `subject_kind` labels follow the new subjects.
14. Tests: classification partition, `PublishFixtureBatch`, publisher-output-
    validates-goldens, ingest change-detect.

## Sequencing (safe order — live path last)

1. subjects + payloads + envelope + publisher + config `source` (no live-path
   touch; publisher unit-tested against goldens).
2. `event.video` in persist (additive; simplest live emit).
3. `ReconcileFixtureOutput` + reconcile classification (additive fields).
4. `PublishFixtureBatch` activity + ActivePoll partition/emit.
5. Ingest change-detect + emit.
6. Rip the old Composer NATS publish + the 6 Kinds. **Safe unilateral edit —
   nothing subscribes to the old subjects today (confirmed 2026-08-14), so this
   breaks no live consumer; the frontend subscribes to the *new* subjects on its
   own schedule.**

## Open decisions + what's easy to miss

These aren't yet nailed — they're the traps:

1. **Catch-up on (re)connect is mandatory, and separate from all of this.** Core
   NATS is fire-and-forget: if a subscriber is down for 5s, every message in that
   gap is *gone* for it. So the consumer MUST do a **full REST window refetch on
   every (re)connect**, then apply live messages — a missed goal heals on reconnect.
   **DECIDED 2026-08-14: core NATS + refetch-on-reconnect is the transport, and it is
   the *correct* model — not an MVP compromise.** A consumer that re-snapshots the
   window on reconnect makes JetStream replay redundant (the refetch already reflects
   the gap). JetStream (#169) earns its keep only for consumers that do NOT refetch
   (nexus event-sourcing, durable webhooks) and to make the BFF↔NATS seam lossless.
   See decisions.md 2026-08-14.
2. **The rip is a *unilateral* one-pass edit — confirmed 2026-08-14.** The frontend
   does not subscribe to NATS at all today, so deleting the 6 old subjects
   found-footy-side breaks no live consumer. All of N1–N8 land in one found-footy
   pass; the frontend *newly* subscribes to the *new* subjects on its own schedule —
   that IS the cutover, and it is frontend-side work, not a found-footy coordination gate.
3. **VAR revocation happens by *absence*.** A removed event drops out of
   `ListByFixture` (removed filter), so on the `fixture.update` refetch the event
   is simply *gone* from the DTO's events. The frontend must treat "event vanished
   from the list" as "revoke it + its clips" (its `s_…` URLs already 410). Confirm
   the bridge/frontend diff handles removal-by-absence, not just additions.
4. **Env identity.** The producer must know it's `dev` vs `prod` to stamp `source`.
   New config field; trivial but required, and it's the in-band env discriminator
   for any cross-env consumer.
5. **Prod = accounts + JetStream, and that's a workspace decision.** Dev is
   no-auth, both connect freely. Prod needs `nsc` per-project(-env) accounts +
   creds + found-footy exporting `found-footy.>` and the bridge importing it —
   plus JetStream for durability. That belongs in the `nats/` repo / dhobley, and
   it's the cutover gate, not the dev test.
6. **`event.stable` for nexus is deferred, safely.** Nexus (pre-warm) will want a
   semantic "goal confirmed" subject. It's pre-operational, so we don't build it
   now — but keeping subjects semantic means it's an additive `Publish` + subject
   later, not a redesign. Don't fold goal-confirmation invisibly into the batch in
   a way that forecloses it.
7. **`event.video` isn't gated on fixture state.** A clip can land minutes after FT
   (discovery outlives the whistle), so `event.video` fires for events on
   *completed* fixtures. Fine — the consumer fetches the event regardless — but the
   frontend must accept a video update for a fixture it's already bucketed as done.

## Scope line

**Dev-MVP (Coppa Italia test):** the 3 subjects, dev no-auth broker, core NATS +
refetch-on-connect, schemas + goldens, producer changes above, frontend bridge in
parallel. **Prod / fast-follow:** JetStream durability, `nsc` accounts + creds +
exports/imports, the shared Go client, metrics dashboards.
