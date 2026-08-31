# FF-081 retained video-quality audit

This command reconstructs the retained perceptual-match and supersession graph,
replays arrival orders, and compares diagnostic keeper policies. It consumes
CSV on standard input. It has no database client, credentials, or write path.

`query.sql` is a read-only Postgres transaction. Export its result once, then
run every policy experiment against that offline file:

```bash
docker exec -i found-footy-prod-postgres \
  psql -qAt -v ON_ERROR_STOP=1 -U ffuser -d found_footy \
  < scripts/audit_video_quality/query.sql > /tmp/ff081-corpus.csv

docker run --rm -i -v "$PWD:/src" -w /src golang:1.25.11-bookworm \
  /usr/local/go/bin/go run ./scripts/audit_video_quality \
  -max-permutations 100000 -details 30 \
  < /tmp/ff081-corpus.csv
```

Production export still requires explicit approval under the project operating
rules. The offline analysis does not.

The total-order scores are comparisons, not accepted production policy. The
focused [2026-08-31 audit](../../docs/design/audits/video-quality-2026-08-31.md)
records the interpretation and rejected transitive-cluster direction.
