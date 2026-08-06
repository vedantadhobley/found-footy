# Twitter scaling — per-event instance model

> **Status:** design, signed off 2026-08-06. **Supersedes** the pool +
> client-side-router + reconcile-loop scaler in
> [`twitter-port.md`](./twitter-port.md) T/d–T/e (see [Supersedes](#supersedes)).
> Not yet built — `cmd/scaler` is still a stub. Tracked by #160 (+ #172 folds in).

## Guiding principle: the monitor is the only poller

API-Football does not push, so the monitor **must** poll — that's unavoidable.
Everything else is **event-driven off the monitor's detections**. The monitor
is the single source of truth and the single state-poller; nothing downstream
independently polls "is this still true?". Concretely, the monitor detects three
things per cycle and they drive the whole system:

- a goal **appears** → (after debounce) spawn discovery,
- a goal **disappears** (VAR / correction) → cancel discovery + revoke its clips,
- a fixture **changes state** → activate / complete.

The only other things that run on a clock are **timers, not state-polls**: the
daily ingest schedule and the cookie keep-alive below. A timer firing is not
polling state — it's a scheduled action.

## The model: one Firefox per event, owned by its EventWorkflow

Instead of a shared pool of N browsers with a router distributing searches
across them, **each active event gets its own dedicated twitter instance**,
provisioned and released by its own EventWorkflow — a per-event checkout, like a
connection pool with a lease.

Why this beats pool + router:

- **No router.** Each EventWorkflow holds its instance's address and talks
  straight to it. The registry + round-robin + Go-connection-reuse problem that
  the pool model needs simply doesn't exist.
- **No contention.** An event's ~15 search attempts get a *dedicated* browser —
  the exact single-browser bottleneck that blocked every clip on 2026-08-05
  (searches queueing behind each other → client-timeout) cannot happen.
- **No memory-leak accumulation.** Instance lifetime = event lifetime (~15 min),
  so Firefox dies long before it bloats (it reached 41 GB over 13 days in the
  old always-on model). The per-instance `mem_limit` + periodic-cycling worries
  mostly evaporate.
- **The lifecycle maps to something that already exists** — the EventWorkflow.
  Acquire on start, release on finish. No separate reconcile loop, no counter,
  no poll.

### Lifecycle

- **Spin up: at debounce-start.** When a *named* (known-scorer) event enters
  debounce, provision its instance immediately. Startup is ~30s (container +
  browser launch + cookie load + auth-verify); the debounce window is ~60–90s
  (3 polls × 30s). So **the instance is warm by the time the event triggers and
  searching begins** — the startup latency hides entirely behind debounce. This
  is the insight that makes zero-warm viable.
  - Unknown-scorer placeholders do NOT provision (they never search — see
    #171 / the debounce-0 placeholder model). Only known-scorer events do.
  - A debounce-started event that *decays* before triggering tears its instance
    back down (small, bounded waste — decays are rare).
- **Tear down: on search-done OR event-removed.** The EventWorkflow releases its
  instance when the search loop completes; a VAR removal cancels the workflow,
  whose cancel-cleanup releases the instance (see [cancellation](#event-driven-cancellation-var-destroy)).
- **Zero warm.** No idle instances between events — the debounce window is the
  warm-up, so a persistent warm browser buys nothing on latency and *costs* the
  leak + cookie-staling it would otherwise avoid. Last event finishes → 0
  instances.
- **Max cap (~8, Python's tested range).** Bounds host resource (~8 × ~2 GB) and
  keeps concurrent sessions on the shared account inside the range Twitter
  tolerated in the Python era.
- **Past the cap: [OPEN DECISION]** — block-and-wait for a free slot (checkout
  blocks; simplest, fair) vs fall back to *sharing* an instance (degrades to the
  pool model under extreme load, more code). Default: block-and-wait.

## Cookie keep-alive (the one non-event spin-up)

Because instances only run during events, an idle stretch (off-season,
international break, a quiet week) never exercises the cookies — and Twitter's
session tokens can go stale, so the next event's fresh instance would load dead
cookies → auth-expired → VNC re-auth. So a **fixture-independent keep-alive**:
check the cookie age on a timer; if older than ~3–7 days, spin up a browser, do
a lightweight verify/scrape (rotates the session), write the refreshed cookies
back, tear down.

**It must live on a schedule that runs regardless of fixtures** — the **daily
ingest schedule** (fires 00:05 UTC every day no matter what) or a small
dedicated keep-alive schedule. **NOT the monitor loop**: the monitor only runs
when there are active/staging fixtures, i.e. it is idle during exactly the
periods where cookies would stale. A timer, not a state-poll, so it respects the
guiding principle.

## Event-driven cancellation (VAR destroy) — folds in #172

When the monitor detects a triggered event's goal **disappeared** (its debounce
decays to 0 → removed), the removal detection *directly drives* the destroy
pipeline — no separate poll for "is this event still valid":

1. **Cancel the EventWorkflow** (`event-{id}`) via Temporal `CancelWorkflow`. Its
   cancel-cleanup stops the search loop and **releases its twitter instance**.
   VideoWorkflow children die with it (`ParentClosePolicy`).
2. **Revoke the promoted shares** — `state='removed', removed_reason='var'` — so
   the read side stops surfacing clips for a goal that didn't happen.

This is wired in the monitor's reconcile absence loop, on `hitZero` for a
`downstream_triggered` event — exactly where `RegisterEventAbsence`'s doc already
says the caller "runs the destroy pipeline (cancel in-flight Temporal workflows,
soft-delete video_shares)", which was never implemented. Live evidence
2026-08-06: Inter Miami 23' Pinter was VAR'd out (debounce 0) but its 2 clips
and its workflow lingered. Both cancel and revoke are idempotent.

## Reaper (safety-net, not a poller)

Temporal's cancel-cleanup activity is *retried*, so the happy path and most
failure paths tear the instance down without help. The reaper catches only a
genuinely-orphaned instance — the workflow was terminated (not cancelled) or the
release activity permanently failed. It **folds into the monitor's existing
cycle** (cross-check running twitter containers vs live EventWorkflows and sweep
the orphans) so it adds no new poll.

## Provisioning mechanism

The EventWorkflow acquires/releases via **activities that drive the Docker API**
(create/start on acquire → return the container's address; stop/rm on release).
That means **the worker (or a dedicated provisioner) needs the Docker socket** —
a tracked host-access requirement (dev: mount the socket; prod: decide the
provisioner boundary). This replaces Python's `docker compose --scale <N>` count
adjustment, because per-event ownership needs *individual* container control, not
a replica count.

## Open decisions

- **Max cap** value (~8?).
- **Past-cap behavior**: block-and-wait vs fall-back-to-sharing.
- **Cookie-refresh threshold** (3–7 days?) and its host schedule (daily ingest vs
  dedicated).
- **Provisioner boundary**: Docker socket on the worker vs a dedicated
  provisioner service (prod security posture).

## Supersedes

- **T/e (instance registry + heartbeat + round-robin router)** in
  `twitter-port.md` — **superseded**. No router in the per-event model; per-event
  ownership removes the need to distribute searches across a shared pool.
- **The `docker compose --scale` reconcile-loop scaler** — **superseded** by
  per-event acquire/release.
- **T/d (rate-limit detection + backoff)** — **still relevant** as an add-on: a
  per-event instance can still hit a 429, and its EventWorkflow should back off /
  surface `rate_limited`. Not part of this core.
- **T/g (session recovery watchdog)** — **subsumed**: a per-event instance that
  crashes mid-search just fails its own EventWorkflow's search activity (retried
  / next attempt), and the reaper cleans the container. No separate watchdog.
