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
