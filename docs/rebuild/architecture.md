# architecture.md — Go rebuild

**Phase F stub.** Populated during Phase D as domain packages land + §9
adapter tree becomes real code.

Target content:

- Domain model — the 8 packages in `internal/domain/` and what each owns
- Adapter tree — the 10 adapters + 1 composer in `internal/infra/`
- Package dependency graph — who imports whom (domain → infra, but never
  infra → domain)
- Workflow hierarchy — 5 workflows in `internal/workflow/`, cross-refs
  to the state machines in `orchestration.md`
- Video pipeline — dataflow from Twitter search to S3, with the atomic
  operations that enforce URL stability + rank uniqueness

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§2 (repo structure), §4 (domain model), §9 (adapters).
