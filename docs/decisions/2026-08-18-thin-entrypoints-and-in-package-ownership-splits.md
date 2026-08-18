# Thin entry points and in-package ownership splits

## Context

FF-045 found two different kinds of structural debt: `cmd/worker/main.go`
owned the complete production object graph, while several large workflow,
activity, repository, service, and test files combined independent
responsibilities. Empty extension packages and compatibility telemetry also
made planned behavior look implemented.

The package taxonomy itself is sound. Moving domain or adapter ownership would
create churn without fixing a runtime boundary, and renaming registered
Temporal functions would add replay risk.

## Decision

- `cmd/worker` owns build identity and the process entry point only.
  `internal/app/worker` owns adapter construction, activity and workflow
  registration, lifecycle order, and schedule reconciliation.
- Large implementation files split inside their existing package at current
  ownership seams. The refactor does not add services, queues, interfaces, or
  concurrency.
- Cross-layer discovery payloads live in `internal/contract/discovery` rather
  than an activity package. Their fields and JSON representation are unchanged.
- Large tests remain beside the package they verify, following normal Go
  layout. Shared package-local fakes stay in a small helper test file; focused
  cases live in responsibility-named siblings.
- A package, method, metric, or vocabulary value with no caller is deleted
  after repository-wide proof. A future feature creates its contract when a
  concrete caller exists.
- Registered Temporal workflow and activity names remain unchanged. File moves
  and package-internal splits must not change replay-visible command identity.
- Dormant live-schema objects are not hidden inside this refactor. FF-054 owns
  their explicit migration and schema-hash boundary.

## Consequences

The executable entry point, EventWorkflow consumer, ingest and monitor
activities, discovery persistence, Twitter search, and Postgres event adapter
now expose their responsibilities through file names. The change is
behavior-preserving and needs no production schema or configuration action.

This is a found-footy decision. If thin entry points become a workspace-wide
project standard, record that separately in `vedanta-dhobley`; this file does
not create a hidden cross-project rule.
