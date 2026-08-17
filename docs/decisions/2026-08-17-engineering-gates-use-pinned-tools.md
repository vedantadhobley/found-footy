# Engineering gates use pinned tool versions

## Context

The Makefile used `golangci/golangci-lint:latest-alpine`. That mutable tag
advanced to golangci-lint v2 while the repository still carried v1
configuration, so `make lint` failed before analyzing any code. The development
Dockerfile also installed Air with `@latest`. Tests still passed while 31 Go
files needed formatting and `go.mod` needed tidying because neither condition
was part of a commit or push gate.

## Decision

- Pin Go `1.25.11` in the Makefile and every Go Docker build stage.
- Pin golangci-lint `2.12.2` and migrate `.golangci.yml` to version 2.
- Pin Air `1.65.3` in the development image.
- `make check-short` runs non-mutating format and module checks, vet, lint, and
  unit tests. The pre-commit hook runs this target.
- `make check` runs the same checks plus the full integration and scenario
  suite. The pre-push hook runs this target.
- Keep `revive` out of the lint set. Its package-comment convention conflicts
  with this repository's deliberate per-file header convention. Documentation
  requirements remain repository policy and code-review responsibility.
- Do not add a global coverage percentage. Tests are added at the boundary of
  each behavior or invariant being changed.

## Consequences

A clean checkout resolves the same compiler, linter, and watcher versions until
an explicit version update changes them. Formatting and module drift fail
without mutating the working tree. Tool updates must change their declaration,
pass the tooling contract test, and leave both check targets green.

This decision changes development and build tooling only. It does not require a
production rollout by itself, although later production image builds use the
pinned Go builder.
