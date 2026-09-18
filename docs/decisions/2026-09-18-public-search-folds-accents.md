# Public search folds accents in PostgreSQL

Date: 2026-09-18

`GET /api/v1/search` owns text matching. Clients send the user's text unchanged
and render the returned fixtures; they do not maintain player, team, league, or
language normalization rules.

Search applies PostgreSQL's official `unaccent` dictionary to both the escaped
query pattern and each searchable value. The existing case-insensitive
substring behavior remains, as does literal handling of `%`, `_`, and
backslash. Stored names and API display values retain their provider spelling,
so `mbappe` finds `Mbappé` without rewriting either value.

The separate Twitter query builder continues using its existing Go
transliteration and token-selection rules. Reusing that discovery-specific
pipeline here would require duplicated normalized columns or an application
scan of database rows. Public search needs accent folding, not Twitter query
expansion, so the database-native operation is the smaller and clearer
contract.

## Production rollout

Release `7386672` deployed on September 18, 2026 UTC. The separately gated
production migration first applied only
`20260918_01_enable_search_unaccent.sql` and verified schema hash
`21d4066ed5e4`. The subsequent application rollout verified both workers, API,
and Twitter at the same commit identity; no frontend or NATS contract changed.

The full release gate passed, including the 366.996-second PostgreSQL suite.
Real-database regressions cover `mbappe`, `gundogan`, `super lig`, `besiktas`,
and `eyupspor` against accented stored values across event, competition, and
team fields. A direct post-deployment production search request remains a
separately approved acceptance action under the production access policy.
