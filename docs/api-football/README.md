# API-Football / API-Sports integration reference

These are attributed project notes, initially checked against vendor v3.9.3
on July 9, 2026. They are not a complete mirror or a grant of vendor rights.

Official sources:

- [API-Football v3 documentation](https://www.api-football.com/documentation-v3)
- [API-Sports football documentation](https://api-sports.io/documentation/football/v3)
- [API-Football terms](https://www.api-football.com/terms) and
  [API-Sports terms](https://api-sports.io/terms)

## Public reference files

| File | Covers |
|---|---|
| [Events](./events-shape.md) | Event types, details, clocks, and per-fixture arrays. |
| [Fixtures](./fixtures-endpoint.md) | Direct endpoint, authentication, response envelope, and queries. |
| [Status codes](./status-codes.md) | Provider status vocabulary and descriptions. |
| [Rate limits](./rate-limits.md) | Quota headers, observed limits, and retry semantics. |

Official documentation defines the documented wire contract. The
[Go adapter](../../internal/infra/apifootball/) defines what this application
currently sends and accepts. These notes preserve the distinction between
documented behavior, captured observations, and inference; recheck drift rather
than treating either the implementation or an old document as universally right.

No raw production-response examples are published here. Inspect production
only within the [operations safety boundary](../operations.md).

## Optional local exports

`vendor/` is ignored local reference storage, not a build, test, documentation,
or release dependency. The existing v3.9.3 PDF and HTML were retained locally
when they were untracked; a fresh clone need not have them. No files were deleted.

The October 21 review found no explicit permission to redistribute the complete
documentation in the official terms or the saved HTML. That is not a finding
that copying is necessarily prohibited; it is a reason not to assume the
project's MIT license covers the exports. See the
[public-source policy](../decisions/2026-10-21-public-source-security-and-references.md).
Historical Git copies remain; untracking is not a history purge.

For private offline reference, an operator may save a browser export under
`vendor/`. Do not force-add those exports or browser assets to public Git.
Respect the vendor's terms and obtain permission before redistributing a full
export. Public documentation must continue to work without those local files.

## Updating a finding

1. Check the official documentation. If bot protection blocks automated access,
   ask for browser verification or inspect an available local export.
2. Record the source/version and whether the finding is documented or observed.
   Do not infer an enum's exact spelling or meaning without evidence.
3. Update the focused note and adapter tests together when behavior changes.
   Record material contract decisions in the [decision log](../decisions/README.md).
