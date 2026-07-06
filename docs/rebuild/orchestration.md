# orchestration.md — Go rebuild

**Phase F stub.** Populated during Phase O as workflows land.

Target content:

- Fixture lifecycle state machine (staging → active → completed) with
  the transition activities
- Event lifecycle state machine — debounce via monitor-workflow
  registration count, VAR removal, download-complete threshold flip
- Discovery trigger — NATS `event.stable` subscriber in
  `cmd/worker/main.go`, JetStream durable consumer semantics
- UploadWorkflow per-event serialization via `SignalWithStartWorkflow`
  + FIFO signal queue

Current design source of truth: [`../rebuild-plan.md`](../rebuild-plan.md)
§5 (orchestration layer), §6 (discovery pipeline).
