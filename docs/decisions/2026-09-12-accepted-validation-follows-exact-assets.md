# Accepted validation follows exact assets

## Decision

FF-093 retains one bounded record per acknowledged accepted evaluation in
`video_asset_validations`. The record belongs to the exact event/MD5 asset,
including a variant that loses its first keeper comparison. It never belongs
to that variant's replacement merely because the replacement receives credit.

This is implemented locally, not deployed. It is the validation foundation for
[reversible selection](../design/proposals/reversible-video-selection.md), not
the graph selector itself. Clock admission, dHash thresholds, quality comparison,
public visibility and ranking do not change.

## Why

Accepted model observations previously expired with Temporal history. A share
retains its own first matching minute, but a never-public variant has no share.
Its keeper's minute cannot establish whether that variant was verified or what
its model actually read. Retaining accepted bytes without their own validation
is insufficient for later selection and clock-policy audits.

## Record and ownership

The activity records:

- Evaluation UUID, event/fixture/MD5 scope, UTC evaluation time and version.
- Actual returned model ID, or an empty value when the server omits it.
- Effective prompt/schema SHA-256 fingerprints and evaluator version.
- Workflow/run/activity/attempt origin, when invoked by Temporal.
- Original expected event time, tolerance, requested frame positions and JPEG
  quality, returned frame observations and the evaluator's complete verdict.

The stored JSON has a 16-KiB database limit. No image, full prompt, raw HTTP
response or workflow history is stored. Requested sampling positions are
separate from returned observations: capture does not tighten the existing
evaluator's admission of nonempty partial responses.

The acknowledged activity output carries the evaluation UUID through placement
retries. The existing event-locked placement transaction inserts the record
with the exact asset and candidate attribution. Reusing an evaluation UUID with
different content fails the whole placement; a genuinely different evaluation
gets a separate record. The repository never overwrites an earlier record.

Exact followers and later sightings reference the asset through existing
`observed_asset_id`; they do not receive copied records or trigger another model
call. This is an asset-to-evaluations association, not a new per-candidate
evaluation foreign key. No new activity or independent write transaction is
introduced. An event-removal decision that wins the placement lock still
prevents acceptance and public mutation.

## Retention and compatibility

Validation follows retained SQL asset history, not Garage object availability
or Temporal retention. Media reclamation leaves it intact. The composite foreign
key enforces asset/event/fixture scope and cascades only if that asset is deleted.
There is no historical backfill: missing past observations remain unknown.

`ff-093-accepted-validation-evidence` version 1 enables capture only alongside
atomic placement and accepted-variant retention. Old recorded histories keep
their old activity payloads and do not manufacture evidence. New histories fail
if an accepted activity result lacks its required record. Rejected and failed
evaluations retain their existing candidate outcome paths. Model calls whose
results were lost before activity acknowledgement are not captured here.

The additive migration is
[`20260912_01_retain_accepted_validation.sql`](../../migrations/20260912_01_retain_accepted_validation.sql).
It creates an empty table and changes no historical clip, credit or share. Both
fresh-schema adoption and upgrade from the previous table set are tested.
Production migration and rollout require separate approval.

## Selection integration

The [selection integration](./2026-09-12-selection-commits-with-incoming-placement.md)
now uses these records in the planner, combined placement transaction and
recovery. A never-public variant uses its earliest retained acceptance; multiple
evaluations do not become a "pick whichever clock helps" rule. Historical
unknowns and missing media remain explicit selection constraints. Each source
retains one canonical alias owner; the subsequent
[direct-support decision](./2026-09-13-popularity-counts-direct-support.md)
separates routing from non-additive per-clip popularity. The integration is local
and undeployed.
