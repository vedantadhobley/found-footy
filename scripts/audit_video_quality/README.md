# FF-081 retained video-quality audit

This command reconstructs the retained perceptual-match and supersession graph,
replays arrival orders, and compares diagnostic keeper policies. It consumes
CSV on standard input. It has no database client, credentials, or write path.

FF-083 exports every retained accepted MD5, including a losing variant that
never received a public share. Such a row uses `share_state=observed` and an
empty `share_id`; its timestamp-verification category is inherited from the
live root reached by committed supersession edges. `superseded_by` remains a
direct decision edge. The command may analyze graph topology but must not treat
connected components as transitive duplicate identity.

`popularity` remains the aggregate credit stored on an asset while it acts as
a root. `observed_popularity`/`*_exact_observations` is the distinct-MD5 source
count derived from candidate `observed_asset_id`; this is the evidence for
comparing how often each encoding occurred.

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

## Human review manifest

Pass `-review-csv` to emit one stable row per direct perceptual match instead
of the text report. The command fills evidence through `current_preference` and
leaves four reviewer-owned columns blank:

- `dedup_decision`: `collapse`, `keep_both`, or `uncertain`.
- `quality_winner`: `left`, `right`, `tie`, `not_applicable`, or `uncertain`.
- `quality_reasons`: semicolon-separated visible reasons such as `cadence`,
  `compression`, `resolution`, `completeness`, `crop`, `screen_recording`,
  `overlay`, or `presentation`.
- `notes`: short evidence that the bounded labels do not express.

Frame rate, spatial bitrate density, and bits per pixel per frame are separate
evidence columns. Never infer the quality winner from one of them alone. A
60 fps clip may spend fewer bits on each frame than a 30 fps clip and still be
the better presentation because it retains twice the motion cadence.

The source tweet URL is selected from immutable `observed_asset_id` when the
FF-083 attribution exists, with old outcome detail as a historical fallback. A
superseded share resolves to its current winner, and a never-public variant has
no share, so reviewers use the source URL to inspect retained evidence when the
tweet still exists.

## Reviewed regression corpus

[`testdata/reviewed-pairs.json`](./testdata/reviewed-pairs.json) preserves ten
accepted pair judgments from the 2026-08-31 review. It contains only derived
dHash sequences, retained technical metadata, human labels, and a snapshot of
the current matcher and comparator results. It contains no video, image, tweet
text, or media URL.

The two outcomes are deliberately separate:

- `human` records whether the presentations should collapse and which one a
  reviewer would retain.
- `current` records what the production matcher and comparator do today.

Tests replay `current` from the stored evidence and validate `human` as an
independent product judgment. A known mismatch, such as J. King's visibly
cleaner short cut losing to the duration-first comparator, is regression
evidence rather than a failing assertion. This lets a future policy measure
which accepted cases it improves without silently rewriting the labels.
