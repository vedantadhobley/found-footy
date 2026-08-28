-- schema-hash: 31d3ad0ecf31dd282b56f03de7bc22286cedb02876b72a85d5da14ab50ac5f3f
-- Adds retry-safe candidate attribution and share identity for FF-066 placement.

ALTER TABLE event_search_candidates
    ADD COLUMN IF NOT EXISTS credited_asset_id UUID
        REFERENCES video_assets(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS event_search_candidates_credited_asset
    ON event_search_candidates (credited_asset_id)
    WHERE credited_asset_id IS NOT NULL;

-- Abort rather than conceal any historical duplicate share identity. The
-- application already expects one share per event/asset; this promotes that
-- contract to a database invariant before the new ON CONFLICT path uses it.
CREATE UNIQUE INDEX IF NOT EXISTS video_shares_event_asset
    ON video_shares (event_id, asset_id);
