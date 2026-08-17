# Found Footy issue closures — 2026-08-17 post-release

This snapshot preserves issues removed from the active register after natural
production workflows satisfied their live-validation conditions. It records
evidence only; current work remains in [`todo.md`](../todo.md).

## FF-034 — candidate evidence and terminal state are one invariant

- **Status:** `closed`; deployed and naturally validated 2026-08-17.
- **Severity:** P1.
- **Original defect:** `StoreCandidate` and `RecordCandidateOutcome` failures
  were discarded, so an EventWorkflow could complete with missing evidence or
  candidate rows still marked `pending`.
- **Implemented contract:** New executions launch candidate work without
  waiting for observation persistence, then require an evidence-carrying,
  idempotent terminal UPSERT before candidate ownership becomes terminal. A
  terminal persistence failure fails the parent and leaves its downstream
  checklist open. Temporal change ID `ff-034-candidate-durability` preserves
  older histories.
- **Rollout:** Commit `f70cfea` deployed at 13:42 UTC. The regression suite
  proves an injected terminal-persistence failure cannot complete the
  downstream checklist.
- **Natural proof:** Elche event
  `a80e663d-178a-4b65-99f5-734f724ccf67` completed all 15 discovery attempts
  with 19 unique candidate rows. Every row was terminal: one `promoted`, eleven
  `rejected`, seven `failed`, and zero `pending`. The workflow completed with
  `outcome_class=assets_surfaced` only after those outcomes landed.
- **Historical data:** Thirty-eight pre-release `pending` rows remain under
  already-completed workflows. FF-034 prevents new rows in that state; it does
  not rewrite history. Any backfill remains a separate production mutation.
- **Relation:** FF-003 uses the durable evidence boundary. FF-046 still owns
  independently serialized ancillary effects.

## FF-051 — rendered Twitter feeds are classified without strict-locator loss

- **Status:** `closed`; deployed and naturally validated 2026-08-17.
- **Severity:** P1.
- **Incident:** All 45 Sassuolo–Cesena searches returned false empty results
  even though known-positive clips appeared inside the configured window.
- **Cause:** Playwright strict locators failed when multiple tweet articles
  rendered before the initial wait resolved. The handler converted every wait
  error into a successful empty feed.
- **Implemented contract:** Wait on the first tweet article, treat only a real
  timeout as `feed_timeout`, return other Playwright failures as typed errors,
  and report `initial_articles`, `tweets_parsed`, `video_tweets`, stop reason,
  and scroll count. The query keeps no server-side time bound; the three-minute
  cutoff remains local.
- **Rollout:** Commit `f2da9a6` deployed at 19:51 UTC. A post-release isolated
  search returned the known Telemundo Lipani clip and full feed diagnostics.
- **Natural proof:** The Elche workflow used the normal three-minute path for
  all 15 attempts and found 19 unique candidate tweets. Attempt 2 returned
  three videos from four parsed video tweets; later attempts reported both
  `age` and `consecutive_seen` stops with rendered-feed counts. This satisfies
  the natural EventWorkflow completion condition and rules out the prior
  false-empty classifier on the observed feed.
