# logging.md — Go rebuild

**Phase F stub.** Populated during Phase S1 alongside the
`internal/observability/logging` package.

Target content:

- Structured JSON emission via
  `logging.Emit(level, module, action, msg, fields...)`
- Vocabulary contract — Module + Action are compile-time enums from
  `internal/observability/vocabulary/`; drift is a compile error
- Field vocabulary — standard field names + types
  (`event_id`, `fixture_id`, `share_id`, `duration_ms`, etc.)
- Loki query cookbook — LogQL snippets for common investigations
- Auto-generated log catalog at `docs/generated/log-catalog.md` per
  [`../rebuild-plan.md`](../rebuild-plan.md) §15.3

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§11 (observability — vocabulary + log-line schema + Loki queries).
