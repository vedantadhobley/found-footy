# Compose partitions the fixed ffmpeg host budget

## Context

The ffmpeg adapter enforces concurrency with a semaphore inside each worker
process. The 2026-08-06 memory decision raised that semaphore to 32 and limited
each subprocess to one thread, correctly matching luv's 32 hardware threads
for one worker. Production later ran two worker replicas without dividing the
limit. Each replica could therefore admit 32 subprocesses, giving the stack a
64-thread nominal load before ffmpeg's incidental work.

The application does not need to know whether it runs in development or
production. The missing input is deployment topology, which Compose already
owns.

## Decision

Production Compose owns a fixed stack-wide ffmpeg budget and its partition:

- 32 hardware threads available to the stack;
- two fixed worker replicas;
- 16 concurrent ffmpeg processes per worker;
- one thread per ffmpeg process.

Compose sets both ffmpeg environment values directly on the worker service, so
they override `.env`'s single-worker defaults. An inert YAML contract test
requires positive explicit values, binds the actual replica and environment
settings to the declared `x-ffmpeg-stack-budget`, and proves
`replicas × processes × threads = hardware threads`.

No environment branch or host-topology logic enters the Go application. The
same binary continues to consume its process-local limits from configuration.

## Consequences

- The fixed production topology cannot overcommit its declared ffmpeg CPU
  budget merely because the worker is replicated.
- Changing the worker replica count requires repartitioning the per-worker
  process cap in the same Compose change; the contract test rejects drift.
- The single dev worker retains the `.env` default of 32 one-thread processes.
- This contract does not make a process-local semaphore elastic. Dynamic
  replica counts require host-shared admission or a dedicated Temporal queue
  with independently controlled workers.
- Landing the repository change does not mutate production. Applying it still
  requires an explicitly approved production deployment.

## Superseded contract

This decision corrects only the fleet-wide arithmetic in the archived
[2026-08-06 worker-memory decision](./archive-through-2026-08-16.md#2026-08-06--worker-memory-mem_limits-streamed-frame-extraction-ffmpegmem-coupling).
Its streamed extraction, per-process thread limit, and memory-circuit-breaker
decisions remain in force.
