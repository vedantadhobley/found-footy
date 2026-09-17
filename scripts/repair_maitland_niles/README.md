# Maitland-Niles bounded restoration

One-event operator repair for the
[reviewed 31/6 preview](../../docs/design/audits/maitland-niles-selection-preview-2026-09-13.md).
This is not a migration, a scheduled task, or a generic historical revalidation
tool. Production execution and notification require explicit approval.

**Completed:** the separately approved [production repair](../../docs/history/maitland-niles-restoration-2026-09-13.md)
committed on September 13 at 22:32 UTC; REST and public SSE confirmed 31/6.
That approval is consumed. Retain these files as evidence, not a routine command
to rerun. Any further production action requires new approval.

## Scope

Only event `01dedc54-da16-4e3c-9539-ac43c81ffd77`, fixture `1557390`, is writable.
The operation retains the four actual fresh accepted evaluations for variants
that never had shares. It preserves all existing share validation, including
the old acceptance of the variant that received a false fresh rejection.

The deployed pure planner generated the proposed selection from the saved
snapshot and these four evaluations. The transaction restores existing share
`s_19a9f2fb7ea2` with compatibility rank 7, changes selected popularity to
31/6/1/1, and updates canonical routing. Only one candidate credit changes;
observed identities, outcomes, details and timestamps remain historical facts.
The API derives public ranks 1/2 and filters the two singletons as usual.

## Safety and receipts

[`repair.sql`](./repair.sql) requires a transaction, one temporary input row,
and operation token `ff081-maitland-niles`. The token prevents accidental
execution; it does not grant authority. The pinned bundle sets a 20-second
statement timeout, 30-second idle-transaction timeout and five-second lock
timeout. It locks the event first, then checks downstream, asset, share and
candidate state in the application's normal ordering.

The SQL rejects changed event data, incomplete downstream work, stale assets,
changed shares/source identities, reclaimed media, unexpected evaluation or
receipt state, different output keepers/scores, and MD5/evidence mismatches.
It compares exact reviewed before-rows and exact proposed after-rows. Full-row
checks also preserve the fixture, its other events and all immutable candidate
fields. Object presence is an external HEAD preflight; SQL flags cannot prove it.

All four evaluations, topology, scores, share restoration, candidate routing and
the receipt commit together. The receipt uses ID
`c1843bcd-030c-5b5b-a980-f74481a8e7ec`; its `Repair` field distinguishes this
operator operation from normal placement and references the new evaluation IDs.
Raw evaluation payloads stay in `video_asset_validations`. The snapshot hash
identifies the original pre-repair state; the request hash also binds the added
evidence and reviewed result. A complete retry is a no-op; a changed post-state
or conflicting receipt aborts. Do not reuse this receipt ID for another action.

Require an acknowledged COMMIT and independent read-back, not merely the
`FF081_REPAIR_VALIDATED` notice. Never replay the accounting just because a
notification failed. [`notify.go`](./notify.go) uses the existing publisher:
`--check` connects without publishing; `--publish-approved` emits the one scoped
`event.update`. It has no SQL or Garage write path. A public SSE check can prove
BFF delivery, but not rendering inside an already-open React tab.

## Rehearsal and retained inputs

[`rehearse.sh`](./rehearse.sh) addresses only the explicitly named isolated
`found-footy-maitland-repair-test-20260913` database, never production. Its
disposable container has no network or persistent volume, a 512 MiB cap,
32 MiB shared buffers, 2 MiB work memory and ten connections. The rehearsal uses
the current schema and a scoped production before-image, not synthetic hashes.

Checks cover rollback success, disabled execution, stale score, event removal,
pending downstream work, reclaimed media, wrong validation MD5, changed rank,
commit, retry/no-op and changed post-state. Every rejected attempt compares a
digest of all seeded rows and evidence tables after rollback. The wrong-MD5 case
fails after evaluation inserts and proves those writes roll back too.

Ignored operation artifacts are under
`scratch-audit-2026-09-13/maitland-niles-review/`: the media/snapshot, raw fresh
evaluations, actual-planner harness, `prepare_repair.go`, generated
`repair-input-v2.json`, `repair-run-v2.sql`, seed data, rehearsal logs and
production verification. Preparation has no service clients. It validates the
evidence, reproduces the reviewed domain plan, and refuses changed inputs.
The earlier nonexecuted bundle retained duplicate evidence in receipt metadata;
v2 references only IDs. Only the checksum-verified v2 bundle is approved for
execution. Neither bundle is routine maintenance.
