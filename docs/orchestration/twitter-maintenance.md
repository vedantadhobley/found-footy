# Twitter maintenance workflow

`TwitterMaintenanceWorkflow` exercises the shared X session and the search DOM
without waiting for a football event. It closes the zero-warm fleet's quiet
period: event browsers remain on demand, while the static fallback performs one
small maintenance cycle every six hours.

## Trigger and ownership

- Temporal Schedule: `twitter-maintenance-scheduled`
- Default cron: `17 */6 * * *`
- Configuration: `WORKFLOWS_TWITTER_MAINTENANCE_CRON`
- Overlap: skip
- Browser target: static `TWITTER_BASE_URL`; no fleet provision or release
- Activity attempts: one; the next schedule tick is the next automatic attempt

Worker startup creates the schedule idempotently. Like the other schedules, an
existing definition is not reconciled when configuration changes; FF-009 owns
that shared schedule-control defect.

## Execution contract

The workflow calls `RunTwitterMaintenance` with stable defaults:

1. `twitter.Client.Verify` posts to `/auth/verify`. The service bypasses its
   60-second warm path, verifies the live session, and requires the current
   cookie snapshot to persist.
2. `twitter.Client.Search` targets the static service with
   `football goal filter:videos` and a local 24-hour maximum age.
3. The activity requires a rendered feed, at least three parsed tweets, at
   least three video-bearing results, and valid HTTPS X/Twitter status URLs.

The successful output—or a failed canary's non-retrying application-error
details—retains the bounded FF-061 result state and network/DOM evidence, stop
reason, initial article count, parsed tweet count, video-tweet count, and
returned-video count in Temporal history. The canary requires `rendered`; an
explicit-empty or unavailable state is a failure because maintenance proves
feed health, not query semantics. An authentication, persistence, or DOM-canary
failure does not retry immediately because that would only repeat X traffic.

This workflow preserves and diagnoses an existing session. It cannot mint new
credentials after full expiry. [FF-059](../todo.md#ff-059--vnc-recovery-uses-the-login-path-x-already-rejected)
owns the separate raw-Firefox operator recovery path.
