-- schema-hash: 51113ceb436f5abebc32dd0fa3bde90f992ee993c1ae7b205a1c59826d058859
-- Retains probed source cadence as an independent video-quality input.

ALTER TABLE video_assets
    ADD COLUMN IF NOT EXISTS frame_rate DOUBLE PRECISION;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'video_assets_frame_rate_positive'
    ) THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_frame_rate_positive
            CHECK (frame_rate IS NULL OR frame_rate > 0)
            NOT VALID;
    END IF;
END $$;

ALTER TABLE video_assets
    VALIDATE CONSTRAINT video_assets_frame_rate_positive;
