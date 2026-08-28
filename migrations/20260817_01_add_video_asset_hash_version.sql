-- schema-hash: 865995d13040e10857351a130d5eb088e4e857d4ffd1baf6f4515f1eb7ff631b
-- Adds an explicit frame-hash contract while the old binary can still write assets.

ALTER TABLE video_assets
    ADD COLUMN IF NOT EXISTS hash_version TEXT NOT NULL DEFAULT 'dhash-v1-unversioned';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'video_assets'::regclass
          AND conname = 'video_assets_hash_version_check'
    ) THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_hash_version_check
            CHECK (hash_version <> '') NOT VALID;
    END IF;
END
$$;

ALTER TABLE video_assets
    VALIDATE CONSTRAINT video_assets_hash_version_check;
