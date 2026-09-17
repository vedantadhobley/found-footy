# Provider health shadow rollout — September 17, 2026 UTC

## Release

Production runs `abfea7a23a7d08acf8ffe4c9ef47cae94613c110` from 02:35:37 UTC
(September 16, 22:35 Eastern). The release contains the durable provider-health
collector and atomic mutation boundary from `89d1d7c`, plus the shadow-default
follow-up. It does not include the separate clock, Twitter extraction, crop,
picture-quality, or historical media-repair work in the main working tree.

The clean detached release checkout is
`/tmp/found-footy-release-abfea7a.rbDJJg8J`. Its `.env` links to the unchanged
project environment. `PROVIDER_INTEGRITY_MODE` is absent there, so the tested
`shadow` default applies. No production Compose, environment, or Caddy file
changed. The code commits are local; this rollout did not push Git.

## Gates and execution

- The exact provider-only snapshot passed unchanged `make check`: formatting,
  dependency metadata, vet, pinned lint, all unit/integration tests, and the
  scenario suite. The PostgreSQL suite completed in 382.615 seconds. The normal
  pre-commit hook passed without bypasses.
- The two September 16 migrations were already applied at 01:24:51 UTC.
  All fourteen ledger checksums and the final schema hash matched the release.
  No migration ran during this application deployment.
- Jeong's 32′ Colorado Rapids–St. Louis discovery completed at 02:24:10 UTC.
  Running event workflows, scoped event browsers, and recent pending candidates
  were zero before recreation. No workflow was cancelled to open the window.
- The unchanged `make deploy-prod` built all application images with identity
  `2026-09-17T02:32:39Z`. Invocation-only wrappers bounded sequential legacy
  Compose builds to 4 GiB, ran the isolated storage smoke, and checked the full
  test result plus live work immediately before recreation.
- Storage and non-root permissions smokes passed. Only synthetic test storage
  was removed. Production worker replicas, API, and static Twitter were
  recreated; VNC remained stopped. Stateful services retained their exact
  container IDs and start times.
- The release exited zero with `release verified` for `abfea7a`.

## Read-only verification

Both workers and API returned healthy responses and the exact SHA/image tag.
Static Twitter reported healthy, verified, idle, and the same identity; startup
cookie backup succeeded. All four processes had zero restarts. Static Twitter
has only the `/config` bind and no anonymous profile volume. Selected startup
logs contained no structured WARN/ERROR/FATAL records.

The first natural active poll at 02:36 UTC persisted a health receipt and five
mutation receipts. Each recorded `Mode=shadow` and applied `Policy=trusted`.
One recommended `positive_only`, proving that recommendation and actual
permission are distinct in the live path. Later polls covered five requested
and five usable fixtures without being ignored. Canonical poll times advanced.

Targeted fixture and event REST reads succeeded. Jeong's event remained
`phase=complete` with one public video. This does not constitute a new-video
pipeline test or confirmation of delivery to a real browser.

## First review item

The first restricted recommendation belongs to Cincinnati–DC United fixture
`1490439`, not the live Colorado match. It is an existing active `NS` fixture
rescheduled to October 21. Stored zero scores versus provider null scores, with
no clock or event evidence, open `populated_field_cleared`. Names and IDs match.
The current empty-score exception covers unchanged `PST`, not `NS`.

Treat this as a potential false restriction to review before enforcement,
alongside FF-088's separate deferred/rescheduled polling issue. Shadow still
permits normal reconciliation; no automatic repair or exception expansion was
performed. Repeated receipts are observations of the same cause, not independent
incidents or lost goals.

## Remaining validation

Keep enforcement off. Review at least one complete match window for freshness,
fixture/provider scope, restricted plans, false restrictions, and cause-specific
recovery. Natural staging/ingest execution, provider-wide restriction/recovery,
and a new accepted-event pipeline still need post-release evidence. Use the
[provider-health ledger](../provider-health.md) and
[FF-075 register](../todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state)
for the current contract and remaining work.
