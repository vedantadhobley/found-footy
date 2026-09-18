# Track Turkish Süper Lig clubs across competitions

Date: 2026-09-18

Found Footy adds API-Football league `203`, the Turkish Süper Lig, to the
default tracked-league roster. Tracked leagues seed teams rather than constrain
fixture competition: once a club is in `tracked_teams_cache`, its domestic,
cup, continental, and other reported fixtures are eligible whenever either
fixture team is tracked.

Turkey lands before Portugal so live Twitter yield, correctness, latency, and
search pressure can be measured without combining two scope changes. The Big
Three and the remaining Turkish clubs should be reviewed separately to avoid
mistaking headline-club coverage for league-wide coverage.

The tracked-team refresh now compares the exact configured league set as well
as cache age. Adding or removing a league therefore refreshes on the next
ingest instead of requiring a manual timestamp backdate or waiting up to 24
hours. Partial-failure preservation remains unchanged.

## Production rollout

Release `280fddc` deployed this contract on September 18, 2026 UTC without a
schema migration. A separately approved normal `IngestWorkflow` run
(`ingest-manual-1789728060`, run
`00567533-caf3-4cfa-a367-2f61ce9b711a`) used `FetchFuture=true`; it was not a
fixture-specific backfill.

The run refreshed the tracked-team roster without error. Its durable result
contained 192 teams, including all 18 returned for league `203`, and 72 staging
fixtures. Read-only verification found nine current/future Süper Lig fixtures:
one on September 18 and four on each of September 19 and 20. Retained UEFA
Champions League and Europa League fixtures for those clubs also remained
eligible, confirming that the league continues to seed teams rather than act as
a fixture-competition filter.
