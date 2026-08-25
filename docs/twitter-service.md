# twitter-service.md — Go rebuild ledger

**Purpose.** As-shipped state of the Playwright-Go Twitter scraping service,
raw-Firefox login terminal, and worker-side Go client. This is the **as-built
ledger**; the design intent
+ the sub-commit history live in
[`design/proposals/twitter-port.md`](./design/proposals/twitter-port.md) (T/a–T/c
shipped) and the signed-off scaling model in
[`design/proposals/twitter-scaling.md`](./design/proposals/twitter-scaling.md)
(#160).

**Update rule.** Any change to the HTTP contract, state machine, cookie model,
or error taxonomy updates this doc in the same commit.

## What it is

A standalone HTTP service wrapping a **Playwright-Go + Firefox** browser. It
does **search only** — given a query, it navigates X's live-search results,
scrolls, and returns the video-bearing tweets within an age window. It never
downloads video bytes; the worker uses the off-browser cookieless syndication
path. Headless event browsers only consume and refresh an existing session.
The opt-in VNC image runs raw Firefox ESR and a separate cookie-capture service;
it contains no Playwright or search endpoint.

The Go client and service are live and have been verified end-to-end against
real Twitter.

## Deployment shape

- **`found-footy-{env}-twitter`** — static fallback + fleet image builder:
  Playwright launches Firefox
  headless; the Go service exposes the HTTP surface on `:8888`. Firefox profile
  lives in the container writable layer (regenerated each start).
- **`found-footy-{env}-twitter-vnc`** — opt-in (`--profile vnc`): raw Debian
  Firefox ESR + Xvfb/noVNC and `cmd/twitter-auth`. Firefox owns the persistent
  environment-specific profile; the Go companion reads `cookies.sqlite` and
  publishes a browser-neutral snapshot. It never searches X.
- Both mount the **shared cookie parent directory** (`~/.config/found-footy/`
  → `/config`); the backup remains `/config/twitter_cookies.json`. The
  directory mount lets atomic temp-file replacement work and survives
  container recreation.

The active search path uses the per-event instance model
([twitter-scaling.md](./design/proposals/twitter-scaling.md), #160): one
short-lived Firefox per searchable event, zero warm. The worker creates each
instance through the Docker API. Compose selects the instance image and network;
the network becomes the opaque ownership scope for names, labels, capacity, and
cleanup (FF-001). The event-only network alias remains deterministic, so
workflows need no registry.

The worker always constructs and injects the HTTP client from static
configuration; construction does not probe the shared browser. Twitter and the
workers can therefore start in either order. Each `/search` call observes live
readiness, and Temporal retries transient startup or connectivity failures
(FF-016).

Fleet provisioning returns after Docker create+start, without waiting for
browser health; Firefox warms behind the event debounce. Temporal retries are
idempotent by scoped event name. An existing running container is reused, while
a stopped container must restart successfully—Docker start failure is returned
to Temporal rather than exposing a dead event address (FF-033).

Firefox is a critical child of the Go service (FF-017). Playwright context
closure or browser disconnection immediately changes service state to
`failed`, emits `twitter.browser_failed`, and makes Go PID 1 exit non-zero.
Compose-managed headless Twitter already uses `restart: unless-stopped`.
Dynamically provisioned event containers use Docker `on-failure`, so the
container layer rebuilds the complete Firefox/Playwright/Go unit and reloads
the shared cookie backup. Application code does not branch on environment or
attempt an in-process browser swap. The opt-in VNC container remains
operator-controlled with `restart: no`.

The search and auth processes have separate typed configuration profiles.
Malformed booleans, relative profile/cookie paths, invalid listen addresses,
and invalid intervals fail before browser work. Re-auth handlers receive
immutable validated values; they do not reread process environment per request.

## HTTP contract

Registered in `service.go RegisterHandlers`:

| Endpoint | Method | Behavior |
|---|---|---|
| `/health` | GET | Browser/auth readiness: 200 only in `healthy`; otherwise 503 with state and reason. |
| `/status` | GET | State, reason, busy flag, fingerprint and load time, nested cookie backup/reload attempt-success-error evidence, startup time, and build identity. Read-only; the release command verifies `build`. |
| `/search` | POST | `SearchRequest{query, max_age_minutes, exclude_urls}` → `SearchResponse` (below). |
| `/authenticate` | GET | Read-only auth status plus the raw-Firefox VNC URL and environment-explicit Compose command. Does **not** force re-auth. |
| `/auth/verify` | POST | Force a live session check, bypassing the 60-second warm path, and require the verified cookie snapshot to persist. Used by maintenance and eventual operator recovery. |

**`SearchResponse`:** `{status, result_state, evidence,
videos:[VideoRef], count, query, stop_reason, scrolls, initial_articles,
tweets_parsed, video_tweets, elapsed}`, where
`VideoRef = {tweet_url, tweet_text, video_page_url, duration_seconds, username,
age_minutes}`. The three feed counters distinguish no rendered feed from a
rendered feed with no hydrated video evidence. Note `video_page_url` is the
tweet page — the worker resolves it to media bytes off-browser via the
syndication adapter; the service returns no CDN URL. The Go client
(`internal/infra/twitter/client.go`) exposes `Search` and the static-service
`Verify` operation.

The wire types live once in `internal/contract/twittersearch`; the browser
service and HTTP client use aliases instead of maintaining JSON-compatible
duplicates. `result_state` is a bounded enum:

| State | Usable search? | Meaning |
|---|---:|---|
| `rendered` | yes | At least one tweet article rendered. Zero video candidates remains valid. |
| `explicit_empty` | yes | X rendered a recognized empty-state selector. |
| `login` | no | Navigation reached X's login/flow route. |
| `upstream_error` | no | SearchTimeline failed or returned 4xx/5xx, or a recognized error/interstitial rendered. |
| `unknown_timeout` | no | No usable feed or explicit state appeared before the bound, or the activity failed without classified page evidence. |

`evidence` retains only the final route with its query removed, a bounded page
title, app-shell/empty/error selector bits, SearchTimeline status or transport
failure, and `x-rate-limit-*` values when X supplies them. It never retains
response bodies, page bodies, request headers, cookies, authorization headers,
or tokens.

The service installs response listeners before navigation, so fast timeline
responses cannot race past the collector. It waits for the first article,
explicit-empty selector, or known error selector. Explicit states therefore
return immediately; only an unexplained absence consumes the full ten-second
bound. The historical `stop_reason=feed_timeout` shape remains for
`unknown_timeout` and rolling compatibility, but it is no longer HTTP success
for attempt-accounting purposes.

## State machine

`State` (`service.go`): `starting` → `loading` (reloading cookies from the shared
backup) → `healthy` / `degraded` / `unauthenticated` / `failed`.
`/status.reason` carries the human-readable why (e.g. `verified`,
`verify failed: …`). A login redirect proves `unauthenticated`; a network or
selector failure is `degraded`, because it does not prove cookie expiry.
Browser/context exit drives `failed` without waiting for a search request;
`failed` is terminal for that process, which then exits so Docker can replace
the unit. Search now marks `healthy` only after a rendered or explicit-empty
observation. A navigation failure, upstream error, or unknown timeout marks the
service `degraded`; a login redirect remains `unauthenticated`. A later usable
search restores `healthy`. FF-039 still owns convergence of this service-specific
state machine with the other binaries' lifecycle contract.

## Auth + cookie fleet model

Cookies are **shared across the fleet via one bind-mounted file**; the filesystem
is the coordination channel (no NATS / pg NOTIFY — decisions.md 2026-07-21):

- **mtime = "cookies changed" signal.** `EnsureAuthenticated` (`auth.go`) runs a
  3-step sequence: **(1) mtime check** — if the backup file is newer than what
  this instance last loaded, reload it into the browser context; **(2) warm path**
  — skip the verify if the last successful `VerifySession` was recent; **(3) full
  verify** — navigate `x.com/home`, look for the logged-in indicator. On success,
  sync the browser's cookies back to the backup file.
- **Atomic writes** via temp + rename, so concurrent readers never see a torn
  file.
- **Fingerprint dedupe** (`cookies_backup.go`): `Fingerprint` is a sha256 over
  the complete sorted persisted cookie shape, including expiry, domain, path,
  and flags. An instance keeps the fingerprint it last wrote in memory and
  **skips the write when nothing changed**. Expiry-only refreshes therefore
  reach disk instead of being mistaken for no-ops.
- **Strict domains.** Only exact `x.com` / `twitter.com` domains and their
  subdomains can enter the shared snapshot; lookalike substring domains cannot.
- **Observable persistence.** Backup and reload attempt, success, and last
  error fields live in `/status`; failure and recovery transitions emit audit
  events. A verified local session with failed persistence stays `healthy` but
  the verify request fails, because a new fleet instance would still load stale
  state.

Net effect: when any authenticated instance refreshes and writes its session,
every other instance picks it up on its next `EnsureAuthenticated` mtime check.
The raw-Firefox capture service publishes new credentials through the same file
contract. Search combines verification inline with search navigation (T/c) to
save ~3–4s per warm call.

### Raw-Firefox recovery

The opt-in VNC container uses the persistent `/data/firefox-profile` volume and
opens X's login flow in raw Firefox ESR. Firefox holds an exclusive lock on
`cookies.sqlite`, so the operator closes Firefox after login. `cmd/twitter-auth`
polls every two seconds, treats the open-browser lock as an expected
`waiting_for_login` state, and captures through SQLite's read-only path as soon
as the graceful close releases it. It requires a non-expired `auth_token`,
drops expired rows, preserves cookie flags, and calls the same strict-domain
atomic writer used by search instances. An unauthenticated, missing, busy, or
malformed profile never overwrites the last known backup.

The capture process exposes read-only `/health` and `/status` on `:8888`.
Status contains state, reason, attempt/capture timestamps, auth expiry, cookie
count, fingerprint, last error, and build identity; it never contains values.
After capture, an operator posts to the static search service's `/auth/verify`
to prove that Playwright loaded and can use the snapshot. The login terminal is
profile-gated and uses `restart: no`. Closing Firefox leaves the capture and
status process running; a capture-process failure ends the incomplete unit.

### Fixture-independent maintenance

`TwitterMaintenanceWorkflow` runs on the independent
`twitter-maintenance-scheduled` Temporal schedule at minute 17 every six hours
by default (`WORKFLOWS_TWITTER_MAINTENANCE_CRON`). It uses the always-running
static fallback; it does not provision an event browser.

One run forces `/auth/verify`, which also writes any rotated cookie state, then
runs `football goal filter:videos` with a local 24-hour age window. It requires
a rendered feed, at least three parsed tweets, at least three video-bearing
results, and structurally valid status URLs. The activity has one attempt, so a
real failure stays visible in Temporal instead of generating an immediate X
traffic burst. This closes the quiet-week maintenance and DOM-canary gap.
Maintenance preserves and diagnoses a session; raw-Firefox VNC owns new login.

## Search result and age classification

The browser sends the broad query exactly as built by Discovery, plus
`src=typed_query&f=live`. It never adds `since:` or `until:`. The service parses
each tweet's timestamp and applies `max_age_minutes` locally; the production
default remains a wall-clock-relative three minutes. This preserves the
recall-tested [query decision](./design/proposals/twitter-search-query.md#d4).

Rendered searches return one of five terminal `stop_reason` values
(`search.go`):

- **`age`** — a non-promoted tweet older than `max_age_minutes` is reached
  (results are reverse-chronological, so everything past it is older too).
  Promoted posts do not terminate the scan because their placement is not
  chronological.
- **`consecutive_seen`** — N consecutive already-seen tweets (from `exclude_urls`
  accumulated across the event's prior attempts) → the good hits are exhausted.
- **`max_scrolls`** — the hard scroll cap.
- **`feed_exhausted`** — a feed rendered, then produced no articles after a
  scroll.

An explicit empty page returns `stop_reason=explicit_empty` and
`result_state=explicit_empty`. An unexplained miss retains
`stop_reason=feed_timeout` for operational continuity but returns
`result_state=unknown_timeout` and is unavailable. Known error selectors or
SearchTimeline request failures/4xx/5xx return `upstream_error`. Locator contract failures,
closed pages, and browser loss remain activity errors; EventWorkflow maps an
exhausted activity call to an unavailable probe.

Scroll jitter is 250–500ms (tightened from 0.5–3s on 2026-08-05), accepts equal
bounds without panic, and is cancellable through the request context.

## Error taxonomy

Six typed transport/service `error_class` values (`internal/twitter/`):
`auth_expired`, `bad_request`,
`empty_query`, `method_not_allowed`, `navigation_failed`, `internal`. Twitter's
own rate-limit error class (`rate_limited`, T/d) remains unused. Natural
production traffic measured SearchTimeline HTTP 429 with limit 50, remaining
0, and a roughly 15-minute reset window. The bounded browser result remains
`upstream_error`; split it only if a concrete workflow policy needs a distinct
semantic state.

## Known gaps

- **FF-059 rollout proof.** Raw Firefox and cookie capture are implemented, but
  the deliberately logged-out dev login → capture → static verify → fresh
  headless reload exercise remains the acceptance gate. See
  [`todo.md`](./todo.md#ff-059--vnc-recovery-uses-the-login-path-x-already-rejected).
- **Concurrent writers are semantic last-writer-wins.** Unique temp files and
  atomic rename prevent corruption, but an older valid browser snapshot could
  supersede a newer one. `AUD-TWITTER-COOKIE-WRITER` holds this for measured
  rotation evidence before adding coordination complexity.
- **Shared admission is not coordinated.** FF-061 preserves page/network
  evidence and prevents unavailable results from consuming logical attempts.
  Natural traffic has now established a 50-request, roughly 15-minute timeline
  bucket on the shared account/IP path. FF-038 owns any coordinated admission
  policy inside the eventual atomic fleet controller.

## Cross-refs

- Design proposal + sub-commit history — [twitter-port.md](./design/proposals/twitter-port.md)
- Search query construction — [twitter-search-query.md](./design/proposals/twitter-search-query.md)
- Per-event scaling — [twitter-scaling.md](./design/proposals/twitter-scaling.md) (#160)
- Cookie-model + login-block decisions — [decisions.md](./decisions.md) 2026-07-21, 2026-07-22
- Raw login boundary — [Raw Firefox owns operator login](./decisions/2026-08-19-raw-firefox-owns-operator-login.md)
- Go client + adapter — `internal/infra/twitter/client.go`
