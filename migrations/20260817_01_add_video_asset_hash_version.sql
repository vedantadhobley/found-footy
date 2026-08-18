-- Adds an explicit frame-hash contract while the old binary can still write assets.
BEGIN;

ALTER TABLE video_assets
    ADD COLUMN IF NOT EXISTS hash_version TEXT NOT NULL DEFAULT 'dhash-v1-unversioned';

ALTER TABLE video_assets
    DROP CONSTRAINT IF EXISTS video_assets_hash_version_check;

ALTER TABLE video_assets
    ADD CONSTRAINT video_assets_hash_version_check CHECK (hash_version <> '');

-- VerifySchema compares the live stamp with the schema.sql embedded in the
-- next worker/API image. This value is replaced with that file's exact SHA-256
-- before the migration is committed.
UPDATE schema_version
SET schema_hash = '865995d13040e10857351a130d5eb088e4e857d4ffd1baf6f4515f1eb7ff631b', applied_at = NOW()
WHERE id = 1;

COMMIT;
