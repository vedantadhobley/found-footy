# Engineering gates follow Git and Go module boundaries

## Context

The root Makefile used recursive filesystem and `./...` discovery. Go does not
honor `.gitignore`, so retained `scratch-audit-*` programs became root-module
packages. Independent audit files can contain several `main` functions or
deliberately rough probes; they broke format, vet, and test gates even though
they were not product source. A new nested module also remained outside the
root module's checks.

## Decision

- Git's tracked plus ordinary-untracked file set defines product source for
  repository engineering gates. Ignored evidence is outside that set.
- Every Git-visible `go.mod` is discovered dynamically and checked in its own
  working directory.
- Build, vet, lint, and test receive concrete package directories derived from
  Git-visible Go files instead of recursive `./...` discovery. Go then applies
  the active build tags within that bounded set, so intentionally tagged tools
  retain normal Go behavior.
- Format checks receive concrete Git-visible Go files.
- Module tidy runs in a temporary Git-visible snapshot. The mutating target
  copies only `go.mod` and `go.sum` back after every module succeeds.
- The pre-commit and pre-push entry points remain `make check-short` and
  `make check`.

## Consequences

Forensic programs remain available without contaminating product gates. A
nested service cannot silently miss root validation, and its dependencies stay
within its own module. New nonignored source is checked before it is staged;
ignored local evidence cannot alter package discovery or module metadata.

This changes repository tooling only. It has no production runtime or rollout
effect.
