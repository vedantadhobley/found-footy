# Go development returns to main

The later [Python-source retirement](./2026-10-21-retire-python-working-tree.md)
supersedes only this decision's working-tree archive retention.

## Context

The Go system replaced Python in production on August 15. Development continued
through `rebuild/go` and subsequent feature branches while GitHub's default
`main` remained on the retired implementation. Both local and remote main tips
are ancestors of the production-tested Go lineage. The current candidate adds
documentation cleanup to application release `02f071a`, not unreleased runtime
changes. The user approved promotion on October 20 and confirmed the repository
is intentionally public.

## Decision

- Use `main` as the canonical Go integration branch. Preserve the complete
  history through a normal fast-forward push; do not squash or force-push.
- Preserve the previous local main tip under the annotated tag
  `archive/python-main`. It includes the older remote main lineage and is a
  source-history reference, not a claimed production image or recovery recipe.
- Leave `feat/event-identity-v2`, `research/ff081-quality-graph` and
  `safety/feat-event-update-pre-sync-20260918` intact. They contain independent
  work and do not enter main merely because the Go lineage is promoted.
- Keep `archive/` unchanged for this promotion. It is excluded from Go images
  and is useful historical behavioral reference. Git already preserves its
  contents; removal from a future working tree is possible after a separately
  approved source/documentation cleanup. Do not delete durable data with it.
- Keep the existing pre-commit and full pre-push engineering gates. No new CI
  service or hosting dependency is required for this branch promotion.

## Operational boundary

Git promotion is not an application deployment. Do not recreate a container,
run a migration, repair data or alter production configuration as part of it.
Running images keep their existing SHA; later releases still use the explicit
clean-commit deployment procedure and production approval boundary.

The archived Python stack is not a supported rollback against current SQL,
Garage or Temporal state. Recovery must account for the exact schema ledger,
retained workflow histories and compatible immutable Go images. Historical
source and old volumes are evidence to preserve, not proof of compatibility.

Repository visibility is intentional. This decision does not change its MIT
license or establish any new rights in third-party media.
