# Vision queue rollout — 2026-10-15

## Scope and release

The user approved implementing dedicated Temporal vision admission and then
deploying it if the checks passed. Application release
`55c2afd32355dd0cc24bd0a1454e65f79fafee80` deployed during a quiet window on
October 15. Both production worker replicas, the API, and static Twitter
reported that exact immutable identity. No migration, provider repair, replay,
new service, schedule, or frontend release was performed.

The [decision](../decisions/2026-10-15-vision-capacity-waits-in-temporal.md)
defines the queue, version marker, unchanged budgets, and rollback boundary.
The [prior-day recovery](./cucho-recovery-2026-10-14.md) preserves the original
OCR, Twitter-budget, and CDN-denial evidence; only the OCR local queue/deadline
interaction changes in this release.

## Validation

- Complete `make check` passed for the main and operator-UI modules: format,
  module state, vet, lint, unit, adapter integration and scenario tests.
- Targeted race tests passed for `internal/app/worker`,
  `internal/infra/temporal`, and `internal/workflow`.
- The real disposable Temporal test filled two vision execution slots, held
  excess tasks scheduled beyond their execution timeout, and then completed
  them on attempt one. General work completed while vision was saturated;
  canceled queued work never executed. Queued work also survived replacement
  of the vision poller. No production server, X search, or model was used.
- Unit/workflow tests pinned old/new routing, unchanged budgets, disabled eager
  execution, inherited chat caps, actual queue logging, startup unwind, and
  simultaneous worker drain.
- The normal pre-commit hook passed. The standard production release script
  built the immutable images, passed the non-root socket/scratch permission
  smoke, and verified all four application process identities.

## Production acceptance

Read-only preflight found zero running EventWorkflow executions and no scoped
event browsers. Application containers started at `08:42:54Z`; both workers
logged vision and general queue startup at `08:42:55Z`, with local chat cap two
and model `gemma-4-12b` unchanged.

Legacy `tctl taskqueue describe` confirmed two activity-poller identities on
`found-footy-vision`, one per new worker container. The bundled modern CLI's
queue-description formatter panicked on this read-only inspection; no
application process crashed. The legacy read was the explicit fallback.

Worker/API health and Control readiness returned HTTP 200. At `08:50:28Z`,
the latest active poll was `08:50:00Z`, with zero candidate rows from the last
day still pending. All four existing schedules remained unpaused; no discovery
execution was running. One database checklist without completion belongs to
`discovery-smoke-f975f431-6d4d-471f-adc6-5607116f3432`, started August 15. It
does not represent current OCR work and was not changed.

The recovered Cucho event still returned its two clips, unchanged dimensions
and clock evidence. Both playback endpoints returned HTTP 302 without printing
signed locations. This checks the retained API/playback contract, not a new
browser, inference, or full-media acceptance.

Container-identity comparison confirmed PostgreSQL, Garage, Temporal, Temporal
PostgreSQL, and Temporal UI retained IDs, start times, and restart counts.
Only the two workers, API and static Twitter were recreated; all four had zero
restarts at acceptance. No private environment, model admission, or stateful
service configuration was changed.

## Remaining proof

No natural OCR candidate had arrived during acceptance. Observe the next burst
for Temporal schedule-to-start age, local semaphore wait, Control admission and
HTTP time, first-clip latency, and durable terminal failure classes. The vision
phase includes Temporal queue time; the local semaphore gauge does not.

This does not reserve shared GPU capacity or fix X allowance exhaustion and
selective CDN 403s. FF-037 remains mitigated rather than wholly closed; broader
ffmpeg admission and capacity guarantees are separate. Old terminal candidate
failures require their own explicitly approved recovery.
