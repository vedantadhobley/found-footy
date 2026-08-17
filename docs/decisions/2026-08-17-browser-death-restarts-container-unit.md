# Browser death restarts the complete container unit

## Context

The archived Python session manager checked whether Selenium's WebDriver was
alive and rebuilt a dead browser from the shared cookie backup. The Go rebuild
removed that loop. Its [per-event scaling proposal](../design/proposals/twitter-scaling.md#supersedes)
declared the session watchdog subsumed because a short-lived event browser
crash was expected to fail its container and let the search activity retry.

That premise was false. Firefox is a child of the Playwright driver below the
Go HTTP process. Firefox can be OOM-killed or crash while Go PID 1 and the
container remain alive. The service then retains a dead persistent context and
can report stale health until another browser operation fails. Static Compose
Twitter has a restart policy, but dynamically provisioned event containers
explicitly used `restart: no`.

## Decision

Firefox is a critical child of the Go Twitter service. Persistent-context close
and browser-disconnect events converge on one idempotent signal. That signal:

1. changes service state to `failed`, making `/health` return 503 and
   `/status.reason` report `browser process exited`;
2. emits one `twitter.browser_failed` audit event; and
3. makes `cmd/twitter` exit PID 1 non-zero.

The container layer owns recovery of the process unit. Compose-managed
headless Twitter retains `restart: unless-stopped`. Docker API-provisioned
event containers use `restart: on-failure`; their next process loads the
existing shared cookie backup. Explicit fleet release still stops and removes
the container and therefore does not trigger a failure restart. The opt-in VNC
container retains `restart: no` because its lifecycle is operator-controlled.
`SearchTweets` transient retries run at roughly 0/10/30/60 seconds, spanning
the measured 30-second cold start even when the browser dies during the final
outer discovery attempt. A Temporal change marker leaves retry attributes
unchanged for histories started before this decision.

No application code branches on environment. Compose selects the image,
network, and fleet configuration; the same browser-criticality rule runs in
every container.

## Consequences

A search active during browser loss fails with the old process. Temporal's
existing activity retry reaches the restarted service instead of repeatedly
calling a dead context. Restarting the full unit also clears Playwright driver
state and avoids concurrent profile ownership, request draining, and state
transfer required by an in-process browser swap.

Repeated browser failure now appears as container restart churn rather than a
silently wedged HTTP process. Operators use `twitter.browser_failed`, container
restart state, and the memory limit to distinguish transient OOM from a corrupt
profile or launch failure.

## Superseded contract

This decision supersedes the scaling proposal's claim that T/g session
recovery was unnecessary because a browser crash would already fail the
container. It does not restore Python's in-process Selenium relaunch loop and
does not change per-event ownership, cookie sharing, or explicit VNC lifecycle.
