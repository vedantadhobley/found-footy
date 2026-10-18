# Control boundary and event-score rollout — 2026-10-18

## Scope

The approved release deployed `02f071a46fc55c0d77c5a429ec4f0b98f7fd3796`
from the clean primary checkout. Two source commits separate the changes:

- `423268d`: [Control errors, retry guidance and request correlation](../decisions/2026-10-17-control-errors-and-request-correlation.md).
- `02f071a`: [provisional event scores independent of aggregate parity](../decisions/2026-10-18-event-scores-follow-observed-timeline.md).

Only the two workers, API and static Twitter were recreated. PostgreSQL,
Garage, Temporal, Temporal PostgreSQL and Temporal UI retained their exact
container IDs, start times and restart counts. The normal build included the
optional VNC image; no VNC container was running or created.

No migration, configuration change, data repair, object deletion, replay,
authenticated X probe, inference load or frontend deployment was performed.
Nothing was pushed to GitHub. The consumer's stable layout for genuinely
unknown goal scores remains a separate Vedanta Systems change; its handoff is
in the score-policy decision above.

## Validation and timing

Both mandatory source-commit hooks passed `make check-short` for the root and
operator-UI modules. Prior focused race suites passed for the LLM adapter,
vision activities/workflows, event presentation, read API and monitor activities.

Preflight found no event/video discovery execution, recent pending candidate
or event browser. A routine active poll was running. Cincinnati–Columbus
(`1490555`) kicked off during preparation without goals or candidate work.
The standard release command rechecked browser ownership before recreation.

The tracked deployment stage began at `21:14:34Z` and completed successfully at
`21:15:04Z`: 30 seconds for build, rollout and release-identity verification.
That excludes source commits/hooks, preparation and subsequent acceptance.
The non-root scratch and Docker-socket smoke passed.

## Read-only acceptance

Acceptance completed at approximately `21:16:02Z`:

- Both workers, API and static Twitter were running, healthy over HTTP and had
  zero restarts. Worker/API build metrics and Twitter's status response all
  identified the full deployed SHA above.
- Scoped startup logs contained no ERROR-level JSON records.
- Targeted REST returned Málaga–Real Sociedad (`1570418`) and the live
  Cincinnati–Columbus fixture. Málaga's seven goals retained their score
  progression from 0–0 through 2–5 and their existing visible clips.
- Retained share `s_d578d174bae7` returned HTTP 302. The signed destination was
  neither followed nor printed; no Garage media download was needed.
- Cincinnati–Columbus resumed polling after recreation. Its database poll time
  was `21:15:30Z`; the later REST response showed first-half minute four, 0–0,
  and no events.

The known historical pending rows and stale future-active fixtures were not
repaired as part of this rollout.

## Remaining evidence

No natural model call had arrived by acceptance. Request-ID correlation and
transient `Retry-After` behavior still need observation on natural calls.
Control queue/backend timing remains a log join, not pure inference timing.

The completed Málaga sample had aggregate/event parity at acceptance. Tests,
not synthetic production mutations or that live sample, establish provisional
score behavior during asynchronous disagreement. Truly ambiguous boundaries
remain null; the frontend must keep the goal-score layout for those values.

Shared Twitter-budget coordination and generic CDN-403 recovery are separate
open work. This release neither changes scrolling nor supplies extra allowance.
