-- schema-hash: d4691198111b3589f196a138ec3d8f7b9e895ba27d4730e7ac239c243f2cf0d9
-- Separates bounded public history from durable SQL and object lifecycles.

ALTER TABLE video_assets
    ADD COLUMN IF NOT EXISTS object_reclaimed_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'video_assets_reclaimed_after_seen'
    ) THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_reclaimed_after_seen
            CHECK (object_reclaimed_at IS NULL OR object_reclaimed_at >= first_seen_at)
            NOT VALID;
    END IF;
END $$;

ALTER TABLE video_assets
    VALIDATE CONSTRAINT video_assets_reclaimed_after_seen;

CREATE INDEX IF NOT EXISTS fixtures_completed_by_kickoff
    ON fixtures (kickoff DESC)
    WHERE state = 'completed';

CREATE INDEX IF NOT EXISTS video_assets_unreclaimed_event
    ON video_assets (event_id)
    WHERE object_reclaimed_at IS NULL;

CREATE INDEX IF NOT EXISTS video_assets_unreclaimed_fixture_event
    ON video_assets (fixture_id, event_id)
    WHERE object_reclaimed_at IS NULL;
