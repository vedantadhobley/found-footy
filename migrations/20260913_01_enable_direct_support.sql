-- schema-hash: 5f1ea2f578c0c4410eb34c7ad3b1c5d84f59fb938c10b7c9e575b1f5f791c35b
-- Accept direct-support receipts and document scores without rewriting historical data.
ALTER TABLE video_selection_commits DROP CONSTRAINT video_selection_commits_record;
ALTER TABLE video_selection_commits ADD CONSTRAINT video_selection_commits_record CHECK ((
    request_hash ~ '^[0-9a-f]{64}$' AND snapshot_hash ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(policy) = 'object'
    AND jsonb_typeof(before_state) = 'array'
    AND jsonb_typeof(result) = 'object'
    AND result#>>'{Plan,Version}' IN ('direct-restoration-v1', 'direct-restoration-support-v2')
    AND octet_length(before_state::text) <= 1048576
    AND octet_length(result::text) <= 1048576
) IS TRUE);

COMMENT ON COLUMN video_assets.popularity IS
    'Selected-clip direct accepted-source support for selection-enabled histories; older histories retain assigned credit. Scores overlap across clips and are not an event source total.';
COMMENT ON COLUMN event_search_candidates.credited_asset_id IS
    'Canonical routing destination and legacy credit owner, not exclusive ownership of direct support. observed_asset_id retains the source byte identity.';
