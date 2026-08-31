-- FF-081 read-only production corpus: retained asset metadata, hashes, and supersession evidence.
BEGIN READ ONLY;

COPY (
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
        a.popularity,
        COALESCE(a.superseded_by::text, '') AS superseded_by,
        s.timestamp_verified,
        s.state::text AS share_state,
        s.id AS share_id,
        e.fixture_id,
        COALESCE(e.player_name, '') AS player_name,
        e.minute,
        COALESCE(e.extra::text, '') AS extra,
        f.home_team_name,
        f.away_team_name
    FROM video_assets AS a
    JOIN video_shares AS s ON s.asset_id = a.id
    JOIN events AS e ON e.id = a.event_id
    JOIN fixtures AS f ON f.id = e.fixture_id
    ORDER BY a.event_id, a.first_seen_at, a.id
) TO STDOUT WITH (FORMAT CSV, HEADER TRUE);

COMMIT;
