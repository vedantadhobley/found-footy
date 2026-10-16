# Explicit CDN-denial rollout — 2026-10-16

## Scope

The user approved committing and deploying the remaining geographic-denial
classifier. Release `6ffee6b8d152e0d43fe683d7f783b2e83d08dfc6` replaced both
workers, the API and static Twitter at approximately `16:47:57Z`.
The earlier [passive accounting](./twitter-accounting-rollout-2026-10-16.md)
remains enabled. The [denial decision](../decisions/2026-10-15-explicit-cdn-geographic-denials.md)
defines the exact response match, byte/time bounds and unchanged unknown-403
retry behavior.

This release classifies explicit denial; it does not make blocked media
accessible. No migration, configuration change, replay, historical candidate
rewrite, authenticated X test, model call or frontend release was performed.
Shared Twitter request admission remains unimplemented.

## Validation and timing

The remaining patch was committed from the primary checkout, leaving it clean
for the standard release command. No temporary release checkout was needed.
The normal pre-commit hook passed both modules' fast gates. Existing focused
adapter/activity/workflow race and regression coverage pins the literal
denial, ambiguous responses, cancellation, bounds, one-attempt terminal
outcome, skipped downstream work and preserved generic-403 retries.

One tracked job recorded explicit start and terminal results:

- Started `16:47:14Z`.
- Commit and required hook: 17 seconds.
- Standard build/rollout/identity verification: 28 seconds.
- Job total, including initial HTTP acceptance: 45 seconds.

The normal release command passed the non-root scratch/socket smoke and
verified all four application identities. The first static-Twitter health
check saw expected warmup HTTP 503; a subsequent read returned HTTP 200 and
`healthy`. The 45-second job timing is not a claim that all browser readiness
checks completed within that interval. The live result was reported before
writing this deployment record.

## Acceptance and remaining evidence

Preflight found no running workflows or scoped event browsers. Both workers
and the API returned health HTTP 200 with zero restarts. Static Twitter
completed warmup, returned health HTTP 200, and was not busy. Targeted fixture
`1584432` retained its REST projection, and retained share `s_1a6c80af981c`
returned HTTP 302 without following or printing the signed location.

PostgreSQL, Garage, Temporal, Temporal PostgreSQL and Temporal UI retained
their IDs, start times and restart counts. No stateful service was recreated.
The current Compose metadata again refers to the primary checkout rather than
the prior temporary accounting-release checkout.

Natural explicit-denial validation remains pending. New literal matches
should become `rejected/cdn_geo_restricted` after one download attempt, with no
hashing or inference. Unknown CDN 403s should still exhaust their normal
retry budget and retain bounded failure detail. Old failures remain unchanged;
FF-089's broader media-recovery investigation is not closed by this release.
