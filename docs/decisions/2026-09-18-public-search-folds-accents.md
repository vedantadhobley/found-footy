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

## Authoritative match provenance

The first rollout selected the correct fixture but returned only the ordinary
fixture DTO. Vedanta Systems then reconstructed team and event highlights with
JavaScript `toLowerCase().includes()`. That second matcher did not fold accents,
so `mbappe` returned Mbappé's complete fixture without marking his event.

The search result now adds typed `search_match` provenance for competition,
home team, away team, and each matching event's player/assist fields. One SQL
statement computes selection and provenance from the same expressions and
snapshot. The ordinary event list remains complete. Consumers only translate
the supplied flags into presentation metadata; they do not inspect names or
repeat normalization. This is an additive REST change with no schema, fixture
visibility, NATS, or search-query change.

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

The later match-provenance correction is tracked as FF-099 and remains a
separate code and consumer rollout until its release is recorded here.
