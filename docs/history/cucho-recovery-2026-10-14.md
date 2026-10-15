# Cucho Hernández recovery — 2026-10-14

## Approved scope

The user approved one production recovery for Cucho Hernández's 74′ goal,
assisted by Antony, in Real Betis–FC Porto:

- Fixture: `1635635`.
- Event: `79b0273f-7d4a-47bc-a384-5d59eed46a37`.
- Operation: `cucho-antony-recovery-20261014`.
- Mode: `recover`, using the deployed `found-footy-ops` entry point.
- Application release: `626469349dcddf1d1441df1f7096a219b3f554a9`.

This approval does not include other event replays, application deployment,
changes to Control admission limits, or changes to search/vision acceptance.
The [operator recovery contract](../operations.md#completed-discovery-recovery)
defines retry identity, preparation, and completion checks.

## Original result

Original discovery closed at `2026-10-14T20:59:32.642314Z` with
`candidates_no_assets`. It completed five usable searches before exhausting
fifteen unavailable observations. Its final evidence showed an unrendered
search page, not an explicit empty feed.

The dry run found 33 existing candidates. Ten are eligible for retry:
nine `cdn_download/forbidden` failures and one validation-activity
`start_to_close` timeout. The other 23 deterministic rejects remain terminal.
The plan grants a fresh usable-search budget and excludes previously seen
candidate URLs. It does not reset the original workflow or recreate the event.

## Related production evidence

Read-only inspection on October 14 separated three existing failure paths:

- **OCR admission/deadline interaction — FF-037/087.** Each production worker
  permits two concurrent chat requests. The inspected Control configuration
  exposed four 12B backend slots but only one node-wide `cold_long` permit.
  Worker logs recorded local semaphore waits of 150–178 seconds inside a
  three-minute validation activity; the subsequent HTTP request inherited
  little remaining time. Gateway samples later completed inference in about
  ten seconds after roughly thirty seconds of gateway admission wait.
  At `20:57:16Z`, forty candidates across eight events had terminal vision
  failures in the preceding hour. These are candidate outcomes, not forty
  proven lost unique clips. The longer day-wide query found 67 vision failures.
- **Twitter admission — FF-038.** The two workers recorded 161 unavailable
  searches out of 261 measured calls over the approximately thirty minutes
  ending near `20:58Z`. Wider retained logs included explicit HTTP 429s with
  limit 50, remaining zero, and a `21:01:01Z` reset. Blank pages with no app
  shell or observed timeline request also occurred; those do not independently
  prove rate limiting. Two inspected event browsers subsequently recovered
  without restarts; cookie reload/backup reported success and neither had an
  OOM or restart.
- **Media access — FF-089.** At `20:57:16Z`, 250 candidates across seventeen
  events had exhausted CDN-forbidden download retries in the preceding hour.
  Three bounded probes returned HTTP 200 syndication metadata but HTTP 403
  for both tested MP4 qualities. Two recently accepted control clips returned
  HTTP 206. All five used the `amplify_video` path family, so that path alone
  does not classify accessibility. Denials were also present on prior days.
  This does not prove a geographic restriction, an authentication requirement,
  or that another download method would succeed.

These findings match [existing deferred issues](../todo.md), not a demonstrated
regression from metadata-insensitive media identity. No inference benchmark,
cookie-content read, browser restart, or provider mutation was used to diagnose
them. Normal fixture polling and some accepted-video placement continued.

## Recovery execution

The approved operation waited behind a bounded gate checking every five
minutes for zero recent pending candidates and zero 12B gateway waiters. An
initial helper exited before any production mutation because Docker did not
forward its SQL stdin. After correcting that helper, the `21:24:51Z` check
found one pending candidate and zero gateway waiters. At `21:29:51Z`, both
counts were zero and the gate ran the approved `recover --apply` operation.

Preparation selected the same ten candidates as the dry run. The operator
verified browser readiness and started:

- Workflow: `event-replay-79b0273f-7d4a-47bc-a384-5d59eed46a37-cucho-antony-recovery-20261014`.
- Run: `ef2ebd53-0e27-4bf4-a5e9-9bced40a2643`.

At `21:35:55Z`, the replay had completed six usable searches with zero
unavailable attempts and had produced two public 1280×720 clips:

| Share | Duration | Clock evidence |
|---|---:|---|
| `s_d08c298e0271` | 83.777 s | Verified broadcast minute 73 for the reported 74′ goal |
| `s_1be5610bda3e` | 63.436 s | Unverified |

Both appeared through the public Vedanta Systems fixture endpoint. Each public
playback endpoint returned HTTP 206 for a 1 KiB byte range. This verifies media
delivery, not a separate human review of the full videos.

The exact replay checklist closed at `21:45:33.642999Z` with
`assets_surfaced`, all fifteen usable searches complete, and zero unavailable
attempts. The operator emitted `EVENT_REPLAY_COMPLETE` with
`checklist_completed=true`, `pending_candidates=0`, and `replay_candidates=10`,
then `RECOVERY_SUCCESS`. A subsequent read-only SQL check independently
confirmed completion and zero pending candidates for the event.

The original missing-video gap is filled and this approved operation is
complete. During execution, the public event reported `complete` while the
operator replay checklist was open, so that public field alone was not evidence
that the recovery execution had finished.

The operator process records explicit completion/failure markers. Final
acceptance requires the exact replay checklist to close with zero pending
candidates, plus a public API check of any returned clips. A completed replay
does not guarantee that every retried source becomes accessible or passes
clock validation.
