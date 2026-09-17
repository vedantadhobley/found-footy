# Clock validation rollout — September 17, 2026 UTC

## Release

Production runs `a2a6830fe8fc67cd2de734ecae2e335172cb1b9a` from approximately
08:01:38 UTC. Build identity is `2026-09-17T07:58:50Z`. The release combines
[FF-094 literal-clock normalization](../decisions/2026-09-15-clock-fields-preserve-visible-evidence.md)
with [removal of unsupported period offsets](../decisions/2026-09-17-relative-clocks-require-visible-period.md)
as `vision-evaluate-v3`.

Running clocks survive allowance fields; secondary timers compose only at
compatible period-ending boundaries. The prompt forbids invented seconds.
Unlabelled half/extra-time offset guesses no longer rescue a mismatching clock.
Explicit reset clocks, continuous clocks, ±1 tolerance and the any-match rule
remain supported. New rejection detail retains evaluator provenance.

No migration, frontend/NATS change, query change, media repair or revalidation
ran. FF-095/096 Twitter extraction changes and picture-quality/crop experiments
remain outside this release. Provider policy remains in its existing shadow
mode. The experimental period-label prompt was not shipped.

## Gates and deployment

- The clean isolated checkout is `/tmp/found-footy-clock-release.o0s4Grhy`.
  Its environment link uses the unchanged project configuration. No production
  Compose, environment or Caddy file changed.
- Normal pre-commit checks passed. The full `make check` passed on the exact
  release tree: format, tidy, vet, pinned lint, unit/integration and scenario
  suites. The initial PostgreSQL suite completed in 371.394 seconds; the final
  exact-tree pass reused valid content-based test results.
- The scoped race suite and saved-clock replays passed before packaging.
  The [period-label report](../design/audits/period-label-recognition-2026-09-17.md#implementation-follow-up)
  separates the four changed benchmark verdicts from the unchanged older corpus.
- Running EventWorkflows, scoped event browsers and recent pending candidates
  were zero before rollout. One old open checklist belongs to the August 15
  `discovery-smoke-f975f431-6d4d-471f-adc6-5607116f3432` run; its history
  is no longer available. It was not classified as live work or changed.
- The unchanged `make deploy-prod` ran with invocation-only Docker memory
  limits. Sequential legacy builds were capped at 4 GiB. Storage and non-root
  permission smokes passed; only synthetic smoke storage was removed.
- Both workers, API and static Twitter were recreated. VNC remained stopped.
  The command exited zero with `release verified` for the exact commit.
- Postgres, Temporal Postgres, Temporal, Garage and Temporal UI retained their
  exact container IDs and start times. No stateful service was restarted.

## Read-only verification

Both workers and API returned healthy responses and exact SHA/build metrics.
Twitter returned `state=healthy`, `reason=verified`, `busy=false` and the
same build identity. All four processes had zero restarts; selected structured
startup logs contained no WARN/ERROR/FATAL records. Static Twitter retains
only its cookie-directory bind and no anonymous profile mount.

Active fixture polls advanced through 08:02:30 UTC after the rollout.
Barcelona fixture `1570385` still returns completed FT 7–2 and ten events.
Targeted Jesus and Yamal reads still return complete events and their existing
two clips each. The wrong Yamal share `s_f1fab5939d08` and wrong-match Jesus
share `s_80a1d7ebe2e8` remain present: deployment does not rewrite acceptance.

## Remaining work and rollback boundary

FF-094 is deployed, validating until a natural candidate exercises v3.
The quiet-window checks do not prove a fresh model-to-frontend event path.
Old exact-byte acceptance can still be reused without a new evaluation.
Historical clip removal requires separate review and per-action approval.
Jesus's matching-clock/wrong-match case remains an FF-003 semantic gap.

The previous application release was `abfea7a`; the schema and migration chain
are unchanged. Any rollback requires fresh approval and a new live-work check;
do not remove durable data or rewrite the migration ledger. Neither release
nor its documentation was pushed to a remote during this rollout.
