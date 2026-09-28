-- schema-hash: 6d72edc944798e7bcafe98e2ff91e1fffcee3d99b6429e7aaf72ac2e6a3d0dd3
-- Allow the versioned identity for encoded media with unknown packet durations.
-- Existing media-v1 identities remain valid and are never rewritten.
ALTER TABLE video_assets
    DROP CONSTRAINT video_assets_media_fingerprint_format;

ALTER TABLE video_assets
    ADD CONSTRAINT video_assets_media_fingerprint_format CHECK (
        media_fingerprint IS NULL OR media_fingerprint ~ '^media-v[12]:[0-9a-f]{64}$'
    );
