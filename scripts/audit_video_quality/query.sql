-- FF-081 read-only production corpus: retained asset metadata, hashes, and supersession evidence.
BEGIN READ ONLY;

COPY (
WITH RECURSIVE lineage AS (
    SELECT
        a.id AS asset_id,
        a.id AS current_id,
        a.event_id,
        a.fixture_id,
        a.superseded_by,
        0 AS depth
    FROM video_assets AS a

    UNION ALL

    SELECT
        lineage.asset_id,
        successor.id,
        lineage.event_id,
        lineage.fixture_id,
        successor.superseded_by,
        lineage.depth + 1
    FROM lineage
    JOIN video_assets AS successor
      ON successor.id = lineage.superseded_by
     AND successor.event_id = lineage.event_id
     AND successor.fixture_id = lineage.fixture_id
    WHERE lineage.superseded_by IS NOT NULL
      AND lineage.depth < 100
), roots AS (
    SELECT DISTINCT ON (asset_id)
        asset_id,
        current_id AS root_asset_id
    FROM lineage
    WHERE superseded_by IS NULL
    ORDER BY asset_id, depth DESC
)
    SELECT
        a.event_id::text AS event_id,
        a.id::text AS asset_id,
        a.first_seen_at::text AS first_seen_at,
        encode(a.md5, 'hex') AS md5_hex,
        a.hash_version,
        encode(a.frame_hashes, 'hex') AS frame_hashes_hex,
        a.width,
        a.height,
        a.duration_ms,
        a.file_size_bytes,
        COALESCE(a.bitrate, 0) AS bitrate,
        COALESCE(a.frame_rate, 0) AS frame_rate,
        a.popularity,
        COALESCE(observed_votes.count, 0) AS observed_popularity,
        COALESCE(a.superseded_by::text, '') AS superseded_by,
        COALESCE(own_share.timestamp_verified, root_share.timestamp_verified, FALSE) AS timestamp_verified,
        COALESCE(own_share.state::text, 'observed') AS share_state,
        COALESCE(own_share.id, '') AS share_id,
        e.fixture_id,
        COALESCE(e.player_name, '') AS player_name,
        e.minute,
        COALESCE(e.extra::text, '') AS extra,
        f.home_team_name,
        f.away_team_name,
        COALESCE(source.tweet_url, '') AS source_tweet_url
    FROM video_assets AS a
    LEFT JOIN roots ON roots.asset_id = a.id
    LEFT JOIN video_shares AS root_share ON root_share.asset_id = roots.root_asset_id
    LEFT JOIN video_shares AS own_share ON own_share.asset_id = a.id
    JOIN events AS e ON e.id = a.event_id
    JOIN fixtures AS f ON f.id = e.fixture_id
    LEFT JOIN LATERAL (
        SELECT count(*)::INT AS count
        FROM event_search_candidates AS c
        WHERE c.event_id = a.event_id
          AND c.observed_asset_id = a.id
    ) AS observed_votes ON TRUE
    LEFT JOIN LATERAL (
        SELECT c.tweet_url
        FROM event_search_candidates AS c
        WHERE c.event_id = a.event_id
          AND (
              c.observed_asset_id = a.id OR
              (c.observed_asset_id IS NULL AND c.outcome_detail ->> 'asset_id' = a.id::text)
          )
        ORDER BY c.outcome_at NULLS LAST, c.discovered_at, c.tweet_url
        LIMIT 1
    ) AS source ON TRUE
    ORDER BY a.event_id, a.first_seen_at, a.id
) TO STDOUT WITH (FORMAT CSV, HEADER TRUE);

COMMIT;
