# observability.md — Go rebuild

**Phase F stub.** Populated during Phase S1 (observability + config
plumbing) alongside the vocabulary package.

Target content:

- Four pillars: logs, metrics, traces, semantic event stream (NATS +
  event_log dual-write)
- Vocabulary catalog — auto-generated from
  `internal/observability/vocabulary/actions.go`
- Metrics inventory — auto-generated from
  `internal/observability/metrics/metrics.go`
- Grafana dashboards + Prometheus alert rules
- Deploy tracking (git SHA + built_at baked in via ldflags)

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§11 (observability).
