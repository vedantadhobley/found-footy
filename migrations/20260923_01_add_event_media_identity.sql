-- schema-hash: 895e2f641e136f693b6428d599715b43bb6bf7f9c1023c0db5f1182f41c91521
-- Exact encoded-media identity is scoped to one event. Legacy assets retain
-- NULL until individually re-observed; no historical evidence is inferred.
ALTER TABLE video_assets
    ADD COLUMN IF NOT EXISTS media_fingerprint TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_media_fingerprint_format'
    ) THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_media_fingerprint_format CHECK (
                media_fingerprint IS NULL OR media_fingerprint ~ '^media-v1:[0-9a-f]{64}$'
            );
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS video_assets_event_media_fingerprint
    ON video_assets (event_id, media_fingerprint)
    WHERE media_fingerprint IS NOT NULL;

ALTER TABLE event_search_candidates
    ADD COLUMN IF NOT EXISTS raw_md5 BYTEA;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'event_search_candidates_raw_md5_check'
    ) THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_raw_md5_check
            CHECK (raw_md5 IS NULL OR octet_length(raw_md5) = 16);
    END IF;
END $$;

COMMENT ON COLUMN event_search_candidates.credited_asset_id IS
    'Canonical routing destination and legacy credit owner, not exclusive ownership of direct support. observed_asset_id identifies the accepted media asset; raw_md5 retains source-byte identity when known.';
