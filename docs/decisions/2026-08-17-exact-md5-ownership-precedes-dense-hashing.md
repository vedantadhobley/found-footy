# Exact-byte ownership precedes dense video hashing

## Context

The signed V-phase design placed `DownloadAndStage → HashVideo` inside one
`VideoWorkflow` child per candidate. EventWorkflow received the exact MD5 only
after dense frame extraction completed. The design acknowledged that MD5 could
gate hashing but deferred the split because exact reposts were expected to be
rare and dense hashing was measured as cheap.

Production invalidated both premises. Two different Huijsen tweet URLs each
downloaded the same 108,216,129-byte file and produced the same MD5. Both child
workflows then ran and retried dense hashing three times. On this 4K input one
attempt occupied ffmpeg until its 100-second bound, so the post-hash exact gate
could not prevent the duplicated expensive work.

## Decision

For new EventWorkflow executions, candidate orchestration splits at the point
where download has returned the exact content identity:

1. EventWorkflow schedules `DownloadAndStage` for each candidate.
2. Its serialized consumer claims `(event_id, md5)` in workflow memory.
3. One claimant schedules `HashVideo`. Byte-identical arrivals wait without
   consuming an ffmpeg slot.
4. On success, waiters transfer their popularity to the claimant and reclaim
   their staging objects. The claimant proceeds to vision.
5. On exhausted hash retries, only that claimant becomes `hash_error`. Its
   staging object is reclaimed and the next waiter takes ownership with an
   independent staging object and full retry budget.

The claim is event-scoped. Cross-event dedup remains outside this change
because validation context, retention, and asset ownership are event-specific.

Change ID `ff-022-pre-hash-md5-claim`, version 1, selects the new command
sequence. Histories without the version retain their original
`ExecuteChildWorkflow(VideoWorkflow)` path. VideoWorkflow and its worker
registration remain until no retained history can replay or resume that path.

## Consequences

- One exact-byte cluster performs at most one active dense hash at a time.
- A bad staging object cannot poison untried byte-identical candidates.
- Download concurrency is unchanged; only redundant dense work is removed.
- EventWorkflow now owns the two candidate activity futures and their terminal
  outcome correlation. Its existing selector and cancellation boundary remain
  the sole mutable workflow-state owner.
- The original V-phase decision to keep download and hash inside every child is
  superseded for new executions. Its rationale remains preserved as historical
  evidence in the design document.

FF-065 later refined step 4's terminal-outcome semantics without changing this
ownership or work-sharing design. See [Exact-byte followers inherit the
representative terminal result](./2026-08-26-exact-followers-inherit-representative-outcome.md).
