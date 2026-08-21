# Twitter maintenance uses the static fallback

## Context

The per-event Firefox fleet is deliberately zero-warm. Event browsers start
behind debounce and disappear when discovery finishes. That removes idle
memory growth, but it also means a week without events creates no browser
traffic to exercise the shared X session.

The scaling decision reserved one fixture-independent cookie keep-alive timer,
but the Go worker never implemented it. The legacy Python system also had an
hourly DOM canary; the rebuild dropped it. The always-running static Twitter
service already provides a browser outside the event fleet and remains the
transport fallback when an event instance is unreachable.

## Decision

Run one `TwitterMaintenanceWorkflow` on an independent Temporal schedule at
minute 17 every six hours.

Each execution targets the static fallback and performs two ordered checks:

1. Force a live `x.com/home` authentication verification and require the
   resulting cookie snapshot to persist successfully.
2. Run a broad `football goal filter:videos` live search with a 24-hour local
   age window. Require a rendered feed, at least three parsed tweets, at least
   three video-bearing results, and structurally valid X/Twitter status URLs.

The activity has one attempt. Repeated immediate retries would add X traffic
without improving the canary signal. The next schedule execution is the next
automatic attempt.

Cookie fingerprinting covers the complete persisted cookie shape, including
expiry and flags. Backup and reload failures are status and audit data. A
verification failure without a login redirect is `degraded`; it is not proof
that the operator must reauthenticate.

## Consequences

- A quiet week still produces regular authenticated traffic, persists any
  cookie rotation, and detects server-side expiry before an event.
- The canary also detects tweet-article, video, and status-link DOM regressions.
- No event browser is provisioned. Dynamic fleet memory and zero-warm semantics
  do not change.
- The static fallback remains a real operational component, not merely a
  nominal emergency URL.
- Temporal history provides durable maintenance evidence even when the static
  service later restarts and loses its in-memory timestamps.

## Separate recovery boundary

Maintenance can preserve and diagnose an existing session; it cannot mint a
new one after full expiry. The current VNC container still launches a
Playwright-instrumented browser, contrary to the locked raw-Firefox login
decision. [FF-059](../todo.md#ff-059--vnc-recovery-uses-the-login-path-x-already-rejected)
owns that separate recovery implementation and proof.
