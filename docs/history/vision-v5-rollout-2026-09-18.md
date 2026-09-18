# Vision V5 rollout — September 18, 2026 UTC

## Release

Production runs `e2aa4a9da4b3e330580d211253ada2dc32cd09e5` from
approximately 09:39:10 UTC, with build identity `2026-09-18T09:36:23Z`.
The release replaces normalized model-inferred periods with literal
`period_label` transcription, conservative Go normalization and exact public
`temperature: 0`. Accepted evidence now retains temperature and reasoning
controls under `vision-evaluate-v5`.

There was no database migration, historical candidate replay, API/NATS or
frontend contract change, Twitter query change, concurrency change, deadline
change or Control configuration change. The previous compatible application
release is `06524ff`; V5 does not rewrite prior Temporal results or accepted
evidence.

## Qualification and gates

- The exact runtime prompt, schema and adapter completed the frozen 39-call
  natural/control screen without a transport, timeout or schema error. Timing
  decisions matched the retained expectations. The known wrong-match and
  graphics-only content limits remain outside this clock-contract change.
- The isolated release branch was `release/ff098-vision-v5`; unrelated dirty
  development work did not enter the images. The checkout reused the unchanged
  gitignored environment file through a symlink.
- Full `make check` passed formatting, module tidiness, vet, pinned lint,
  unit, integration and scenario tests. PostgreSQL integration completed in
  369.867 seconds. The commit hook reran the fast gate successfully.
- The standard deployment used capped sequential legacy builds. The isolated
  Twitter storage smoke and non-root Docker-socket/`/scratch` permission smoke
  passed before recreation.
- Read-only preflight found no event pipeline rows or scoped production Firefox
  containers. The deployment script repeated its fleet check after building.

## Production verification

The standard release target recreated only both workers, API and static
Twitter, then printed `release verified` for the exact SHA. All four processes
report the expected identity and have zero restarts. Both workers connected to
`http://control-joi.luv/v1` with `gemma-4-12b` and two local slots each. The
first active poll completed with zero errors and no event changes.

Postgres, Garage, Temporal, Temporal Postgres and Temporal UI retained their
August container start times and zero restart counts. VNC remained stopped.
One advisory provider-integrity warning on the future October MLS fixture was
handled in shadow mode with no write; it is unrelated to V5.

Natural candidate validation remains open. No FF-097 historical recovery is
authorized by this rollout.
