# Maitland-Niles 90+6′ restoration — 2026-09-13

## Result and scope

The user approved one production repair and its `event.update` after the
[fresh-validation preview](../design/audits/maitland-niles-selection-preview-2026-09-13.md).
The transaction committed at **22:32:12.164941 UTC** for Everton–Manchester
United fixture `1557390`, event `01dedc54-da16-4e3c-9539-ac43c81ffd77`.
Independent REST read-back returns:

| Public rank | Existing share | Duration | Direct support |
|---|---|---:|---:|
| 1 | `s_52350ba4e42f` | 47.577 s | 31 |
| 2 | `s_19a9f2fb7ea2` | 7.056 s | 6 |

The focused original is restored. Both clips show the same goal; their retained
direct hashes fail the current match windows. This repair does not establish a
new angle, improve the quality comparator, or change matching thresholds.
Two other selected clips retain support one and remain hidden by FF-078.

No deployment, schema change, workflow replay, restart, or production media
deletion occurred. The separate FF-094 clock defect remains open.

## Applied state and preserved evidence

The scoped [operator SQL](../../scripts/repair_maitland_niles/README.md) retains
four actual fresh accepted evaluations for never-public variants. Their asset
prefixes are `3227d1d5`, `358b5db8`, `b609a521`, and `d0ef0afe`; evaluation
timestamps remain the actual 22:02–22:03 UTC observations, not September 6.
Existing share validation remains unchanged, including the older acceptance of
the variant that falsely rejected during fresh evaluation.

The same transaction changes five asset rows: restores the original root,
updates the current keeper's support from 32 to 31, and flattens three hidden
edges to that keeper. It restores the original share with compatibility rank
7; the API derives public rank 2. One candidate's credit follows its original
asset. All 34 accepted-source identities, candidate outcomes, details and
timestamps remain unchanged. Five source observations directly support both
public keepers, so selected support totaling 39 is not a distinct-tweet count.

The event remains complete. Its `updated_at` remains
`2026-09-06 16:00:30.839293+00`, and fixture `completed_at` remains
`2026-09-06 16:00:30.816422+00`. Other events and the fixture row are unchanged.

Receipt `c1843bcd-030c-5b5b-a980-f74481a8e7ec` binds the pre-repair snapshot,
four new evaluations and the reviewed plan. Its request hash is
`01fe4aa226222f76c46c59ebfb06e76f4b45b065a6817d78ed793690ebf90af7`.
All evidence, selection, support, routing and share changes committed together.

## Rehearsal and execution gates

The exact SQL ran against the current schema and a scoped full-fixture
before-image in isolated disposable Postgres. Rollback, commit and complete
retry/no-op passed. Eight negative cases rejected disabled execution, stale
score, removed event, pending downstream work, reclaimed media, wrong evidence
MD5, changed rank and changed post-state. Each failure preserved the full seeded
database digest; the wrong-MD5 case proved rollback after evidence insertion.
The final rehearsal marker was `REPAIR_REHEARSAL_SUCCESS`.

Fresh HEAD checks at 22:30:13 UTC returned 200 and expected sizes for all ten
originals. The target's downstream work was complete, with no pending candidates.
An unrelated discovery was running; it was outside this repair's locked scope.
The target remained inside the media-retention window. NATS connection and
public SSE connectivity were checked before the write.

Production returned an acknowledged `COMMIT` and exit zero. Independent SQL
and REST reads confirmed the receipt, evidence and final public projection.

## Notification and media verification

Exactly one `found-footy.prod.event.update` was published at **22:34:46 UTC**,
message `2a567059-bc44-44d1-aaf7-ddf5d8ab5f5a`. The publisher completed its
broker round-trip. Public SSE received the matching event with both shares,
ranks 1/2, support 31/6 and completed monitor/download flags. No fixture update
was published. This proves delivery through the BFF, not rendering inside an
already-open user browser.

The restored media endpoint returned 302 to its original object key. Its
`public, max-age=240` cache policy can leave an earlier cached redirect in use
until that entry expires. No signed URL or credentials are retained in this doc.

## Retained artifacts

Ignored evidence lives under
`scratch-audit-2026-09-13/maitland-niles-review/`: originals, sampled frames,
raw evaluations, full-fixture before-image, planner preview, frozen SQL/input,
rehearsal logs, transaction log, SQL/REST read-back and public SSE event.

SHA-256 identifiers:

- Executed `repair-run-v2.sql`:
  `eabeee3286305210de0ac0d9693b37da111d18fe9b3b229b1746a7f510e49bed`.
- Transaction log:
  `35357811b96573bad8e870476194d13b2562612364c538de19dfd1ab46ba3f2e`.
- REST read-back:
  `f5c1dbd69c0f2fedc9cd0e3d4323bb2e7646e9575f4cb12c3e089a01269f64bf`.

Only the disposable rehearsal database was removed after verification; its
tmpfs state is gone, and the retained seed can recreate it. The bounded SSE
listener ended at its configured timeout after recording successful delivery.
The approval is consumed. Do not rerun or expand this repair without new approval.
