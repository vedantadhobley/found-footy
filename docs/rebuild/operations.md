# operations.md — Go rebuild

**Phase F stub.** Populated during Phase S onward as bring-up and
failure-mode procedures become real.

Target content:

- Bring-up runbook: fresh clone → `.env` → `make dev-up`
- Prod deploy runbook: `bin/deploy` steps, safety checks, rollback
- Common failure modes + diagnostic paths (postgres saturated, LLM
  endpoint down, dual-write skew alerts, JetStream consumer lag)
- Scaler tuning guide
- Twitter cookie re-auth flow (VNC profile bring-up + procedure)
- On-call playbook

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§10 (deployment), §14 (cutover — includes early-stage ops procedures).
