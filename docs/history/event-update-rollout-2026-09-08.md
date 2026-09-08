# Event-update production rollout — 2026-09-08

The [coordinated hard cutover](../decisions/2026-09-08-event-update-uses-coordinated-cutover.md)
deployed the FF-085 producer and FF-086 consumer during the quiet window.

## Release identities

- Found Footy: `3723ce2aa6476a2c85e2bb24351336eda890683d`, including producer
  `dbc2a76`; built at 13:31:45 UTC. Both workers, API, and Twitter started at
  approximately 13:32:12 UTC and passed the release-identity checks.
- Vedanta Systems: clean checkout
  `ca1f8e52ff066f1d0355653ff33a8db11fced6dc`. Only its API and frontend were
  recreated, at approximately 13:32:49 UTC. The matching frontend commit is
  deployed locally; this rollout did not push its branch.
- Shared contract: `fcfb28f`. The schema repository has no remote configured;
  its committed JSON schemas remain local. This is not a runtime dependency
  or a database migration.

Frontend image:
`sha256:4c450e3914f2a92da1c96b66ef8ca9865e8d438553aec04d43db254b72ee2aa4`.
BFF image:
`sha256:d4da76b457c7c4622b28079518e986dd32f60f3625bb9b15652618a2fe970bb1`.
The running container image IDs matched the staged builds. The BFF route and
shared client-state source hashes also matched the clean checkout.

## Checks performed

- Temporal listed zero running EventWorkflow executions before and after
  rollout. No production event browsers or named debouncing events were
  present at preflight. The next scheduled kickoff was 16:45 UTC.
- The Found Footy release script passed its non-root Docker-socket and scratch
  permission smoke test, then verified all four running application identities.
- Worker and API startup verified the existing migration ledger and required
  schema. No migration, fixture/event repair, workflow replay, or Garage
  mutation was performed. Databases, Temporal, NATS, and btop were not recreated.
- Public global health and Found Footy health returned HTTP 200 and healthy
  state. The public fixture endpoint returned a valid snapshot.
- Browser-style public HTML referenced `/assets/index-ivFie9qv.js` and
  `/assets/index-B5hvfBdf.css`, matching the new frontend build. The default
  curl user agent takes the separate crawler/OG path and is not a bundle check.
- NATS reported the new BFF connection, ID `1320`, subscribed to
  `found-footy.prod.>` with zero pending bytes. Its log confirmed connection.
- A public SSE request traversed ingress and returned HTTP 200,
  `text/event-stream`, and the initial `connected` and `health` messages.

## Remaining acceptance

This verifies deployment, source identities, REST, NATS subscription, and the
public SSE handshake. It does not prove a natural event update reached and
changed React state. No synthetic production event was published, and no
physical browser or iPhone acceptance was performed.

Reload already-open browser tabs onto the new bundle; an SSE reconnect alone
does not replace old JavaScript. During the next natural fixture, correlate
producer publication, BFF receipt/fetch/SSE diagnostics, and client application.
In particular, verify no-candidate discovery changes `searching` to `complete`
without a page refresh. FF-085 and FF-086 remain `validating` until that proof.

Postponed Cincinnati–DC United (`1490439`) remains unchanged and polled; this
release neither force-completes it nor changes deferred-fixture polling policy.
