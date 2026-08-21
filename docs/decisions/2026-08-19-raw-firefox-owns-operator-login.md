# Raw Firefox owns operator login

## Context

The July login experiment proved that X blocks the username step when Firefox
is launched through Playwright, while the same session remains usable for
post-login scraping. The signed design therefore split responsibilities: raw
Firefox mints credentials; Playwright consumes them. The deployed VNC service
had drifted back to the Playwright search image with `headless=false`, so the
documented full-expiry recovery path could not satisfy its own evidence.

## Decision

Use two explicit images and processes:

- `docker/twitter/Dockerfile` is headless Playwright Firefox and serves search.
  It contains no VNC packages or VNC runtime branch.
- `docker/twitter-auth/Dockerfile` is opt-in raw Debian Firefox ESR plus
  Xvfb/noVNC. It contains no Playwright driver and never serves search.

The VNC image runs `cmd/twitter-auth`, a focused capture service. Firefox ESR
holds an exclusive lock on `cookies.sqlite`, so the operator closes Firefox
after completing login. The service polls the database read-only, reports the
open-browser lock as `waiting_for_login`, then captures as soon as the graceful
close releases it. It requires a non-expired `auth_token`, drops expired
cookies, preserves domain/path/expiry/security/SameSite fields, and publishes
the existing JSON envelope through the same strict-domain atomic writer used
by search instances. Unauthenticated or failed reads never replace the shared
backup.

The capture service exposes only read-only `/health` and `/status`. Its status
contains capture timestamps, auth expiry, count, fingerprint, error, and build
identity—never cookie values. A successful capture proves profile extraction;
the operator then forces `/auth/verify` on the static search service to prove
that Playwright can load and use the new snapshot.

## Consequences

- Login and scraping no longer share a browser-automation process or image.
- The login terminal stays profile-gated and zero-cost while not needed.
- The raw Firefox profile remains in the existing environment-specific named
  volume; only the browser-neutral cookie file is shared across environments.
- The production release verifier can still identify an already-running VNC
  container through `/status.build`.
- The existing `/authenticate` URL and Compose command now point to a recovery
  path that matches the locked design.
- This supersedes the 2026-07-22 one-image/`WITH_VNC` implementation decision;
  that reconciliation was based on scraping-shape parity and contradicted the
  login-specific experiment recorded the same day.
