# twitter-service.md — Go rebuild ledger

**Purpose.** As-shipped state of the Playwright-Go Twitter scraping service
(`internal/twitter/` + `cmd/twitter/`) and its Go client
(`internal/infra/twitter/`). This is the **as-built ledger**; the design intent
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
downloads video bytes (that's the worker's off-browser cookieless syndication
path) and its browser is never used to *log in* (Twitter blocks login through a
Playwright-instrumented Firefox — see `AUD-0813-CF-153` in
[`todo.md`](./todo.md#audit-intake-requiring-current-code-validation)).

The Go client and service are live and have been verified end-to-end against
real Twitter.

## Deployment shape

- **`found-footy-{env}-twitter`** — static fallback + fleet image builder:
  Playwright launches Firefox
  headless; the Go service exposes the HTTP surface on `:8888`. Firefox profile
  lives in the container writable layer (regenerated each start).
- **`found-footy-{env}-twitter-vnc`** — opt-in (`--profile vnc`): same binary +
  Xvfb + x11vnc + noVNC, for the **operator to log in manually** (the only way
  to mint fresh cookies) and for debugging.
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

The service reads its listen address, profile, cookie path, headless/VNC mode,
and re-auth instructions through the typed Twitter configuration profile.
Malformed booleans and the impossible VNC-plus-headless combination fail
before Firefox launches. Re-auth handlers receive immutable validated values;
they do not reread process environment per request.

## HTTP contract

Registered in `service.go RegisterHandlers`:

| Endpoint | Method | Behavior |
|---|---|---|
| `/health` | GET | Browser/auth readiness: 200 only in `healthy`; otherwise 503 with state and reason. |
| `/status` | GET | `{state, reason, busy, cookie_fingerprint, last_auth_check, last_loaded_mtime, started_at, build:{git_sha,built_at,image_tag}}`. Read-only; the release command verifies `build`. |
| `/search` | POST | `SearchRequest{query, max_age_minutes, exclude_urls}` → `SearchResponse` (below). |
| `/authenticate` | GET | Read-only auth status + the VNC re-auth URL and environment-explicit Compose command. Does **not** force re-auth. |
| `/auth/verify` | POST | After an operator logs in via VNC, verify the session succeeded → resume the Playwright context. |

**`SearchResponse`:** `{status, videos:[VideoRef], count, query, stop_reason,
scrolls, elapsed}`, where `VideoRef = {tweet_url, tweet_text, video_page_url,
duration_seconds, username, age_minutes}`. Note `video_page_url` is the tweet
page — the worker resolves it to media bytes off-browser via the syndication
adapter; the service returns no CDN URL. The Go client
(`internal/infra/twitter/client.go`) exposes only `Search`.

## State machine

`State` (`service.go`): `starting` → `loading` (reloading cookies from the shared
backup) → `healthy` / `unauthenticated` / `failed` (browser dead / unrecoverable).
`/status.reason` carries the human-readable why (e.g. `verified`,
`verify failed: …`). Browser/context exit drives `failed` without waiting for a
search request; `failed` is terminal for that process, which then exits so
Docker can replace the unit.

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
  the sorted `(name, value)` cookie pairs. An instance keeps the fingerprint it
  last wrote in memory and **skips the write when nothing changed** — kills the
  60–80% no-op writes Python did.

Net effect: an operator re-auths **once** on the VNC container (or any instance
refreshes its session token), writes the backup file, and every other instance
picks it up on its next `EnsureAuthenticated` mtime check. Search combines the
verify inline with the search navigation (T/c) to save ~3–4s per warm call.

## Search scroll-stop conditions

The scroll loop halts on one of four `stop_reason`s (`search.go`):

- **`age`** — a tweet older than `max_age_minutes` is reached (results are
  reverse-chronological, so everything past it is older too).
- **`consecutive_seen`** — N consecutive already-seen tweets (from `exclude_urls`
  accumulated across the event's prior attempts) → the good hits are exhausted.
- **`max_scrolls`** — the hard scroll cap.
- **`empty`** — no tweets rendered.

Scroll jitter is 250–500ms (tightened from 0.5–3s on 2026-08-05).

## Error taxonomy

Six typed `error_class`es (`internal/twitter/`): `auth_expired`, `bad_request`,
`empty_query`, `method_not_allowed`, `navigation_failed`, `internal`. Twitter's
own rate-limit class (`rate_limited`, T/d) is **not built**.

## Known gaps

- **`AUD-0813-CF-153` — no self-recovery from full cookie expiry.** If the shared cookies
  fully expire, the service can only report `unauthenticated` and wait for fresh
  cookies to arrive via the backup file (an operator VNC login). The Python
  raw-Firefox manual-login subprocess (`_launch_manual_firefox`) is not ported.
  See [`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).
- **`AUD-TWITTER-RATE-LIMIT` — rate-limit detection and backoff** are unbuilt
  feature scope. See
  [`todo.md`](./todo.md#audit-intake-requiring-current-code-validation).

## Cross-refs

- Design proposal + sub-commit history — [twitter-port.md](./design/proposals/twitter-port.md)
- Search query construction — [twitter-search-query.md](./design/proposals/twitter-search-query.md)
- Per-event scaling — [twitter-scaling.md](./design/proposals/twitter-scaling.md) (#160)
- Cookie-model + login-block decisions — [decisions.md](./decisions.md) 2026-07-21, 2026-07-22
- Go client + adapter — `internal/infra/twitter/client.go`
