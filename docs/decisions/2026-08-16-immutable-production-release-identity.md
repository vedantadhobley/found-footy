# Production releases use one immutable identity from a clean checkout

## Context

The worker and API binaries already accepted `GIT_SHA` and `BUILT_AT` build
arguments, but production Compose did not supply them or `IMAGE_TAG`. Running
workers therefore reported `git_sha="unknown"`, an empty image tag, and
`built_at="unknown"`. Container creation time could suggest a revision but
could not prove one. Twitter discarded the metadata entirely.

The historical deployment sketch in
[`rebuild-plan.md` §10](../design/rebuild-plan.md#10-deployment) checked out and
pulled a branch on the production host, used mutable image tags, and verified
only the API. That procedure cannot prove that the Docker build context exactly
matches the identity attached to every running service.

## Decision

`make deploy-prod` is the sole repository-owned application release command.
It resolves the full SHA of the clean commit already checked out and one UTC
build timestamp. It does not fetch, pull, or switch branches. Non-ignored
untracked files also make the tree dirty. The command rechecks both HEAD and
cleanliness after the image build and before the first disruptive operation.

The full SHA is the immutable image tag for worker, API, Twitter, and Twitter
VNC. Compose passes the same SHA and timestamp into every binary and passes the
same tag through `IMAGE_TAG`. The worker's `FIREFOXFLEET_IMAGE` points at the
SHA-tagged Twitter image, so later per-event instances use the same release.

The command refuses rollout while a Firefox event container is running on the
production network. This preserves in-flight searches and prevents a mixed
Twitter release. It rebuilds Twitter VNC on every release and recreates it only
when it was already running.

After recreation, the command verifies both worker replicas and the API
against their Prometheus deploy-info gauges. Twitter and an already-running
VNC instance expose the same identity in `/status`; the command verifies those
responses. A mismatch or missing replica fails the release. The command does
not attempt an automatic rollback.

## Consequences

- A running application process can be mapped to an exact source commit,
  image tag, and build time.
- A dirty or concurrently changing checkout cannot produce a production
  release.
- Production rollout must wait for active event searches to finish.
- `latest` remains only a Compose interpolation fallback for manual model
  inspection; the release command never uses it.
- Running the command is a production mutation and still requires explicit
  per-action approval. Adding the command does not grant standing authority.
- Rollback stays an observed-state operation. The immutable old image remains
  addressable, but the operator must choose the target after diagnosing the
  failure.

## Superseded contract

This decision supersedes the branch-mutating, partial-verification deployment
sketch in the historical
[`rebuild plan`](../design/rebuild-plan.md#10-deployment). The current procedure
is recorded in the [`deployment ledger`](../deployment.md#deploy-tracking) and
[`operations runbook`](../operations.md#production-rollout-and-rollback-gates).
The defect and local implementation state are tracked as
[`FF-019`](../history/issue-register-2026-08-17.md#ff-019--production-images-do-not-carry-verifiable-build-identity).
