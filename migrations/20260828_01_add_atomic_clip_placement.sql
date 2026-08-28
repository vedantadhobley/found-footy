-- Adds retry-safe candidate attribution and share identity for FF-066 placement.
BEGIN;

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

UPDATE schema_version
SET schema_hash = '31d3ad0ecf31dd282b56f03de7bc22286cedb02876b72a85d5da14ab50ac5f3f', applied_at = NOW()
WHERE id = 1;

COMMIT;

-- Rollback compatibility: the prior binary ignores credited_asset_id and the
-- additional unique index, but its drift guard knows only the prior schema
-- fingerprint. Before starting that image, deliberately restamp:
-- UPDATE schema_version
-- SET schema_hash='b479df7bb3567870ec0ec4320c37f52821cb738bf2358f40b6e897ac52af1447', applied_at = NOW()
-- WHERE id = 1;
-- Do not drop additive objects during the rollback window.
