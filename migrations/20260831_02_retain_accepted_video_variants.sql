-- schema-hash: d680ec63b34a46db1f42a3fe549926c4adee8b6d38daa2c8d73f29a18715efae
-- Retains accepted MD5 variants and separates observed bytes from credited winners.

ALTER TABLE event_search_candidates
    ADD COLUMN IF NOT EXISTS observed_asset_id UUID;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'event_search_candidates_observed_identity_fkey'
    ) THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_observed_identity_fkey
            FOREIGN KEY (observed_asset_id, event_id, fixture_id)
            REFERENCES video_assets (id, event_id, fixture_id)
            ON DELETE RESTRICT
            NOT VALID;
    END IF;
END $$;

ALTER TABLE event_search_candidates
    VALIDATE CONSTRAINT event_search_candidates_observed_identity_fkey;

CREATE INDEX IF NOT EXISTS event_search_candidates_observed_asset
    ON event_search_candidates (observed_asset_id)
    WHERE observed_asset_id IS NOT NULL;
