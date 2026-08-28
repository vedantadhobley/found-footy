-- schema-hash: d2582f3d96850412bfaffa2fcdc812275d1792a0cd9b7b94412a738b418eaee0
-- Enforces cross-table event/fixture identity and durable media/state bounds.

-- Refuse ambiguous history. The migration never guesses which parent or
-- timestamp an inconsistent row should have carried.
DO $$
DECLARE
    invalid_count BIGINT;
BEGIN
    SELECT count(*) INTO invalid_count
    FROM video_assets a
    LEFT JOIN events e ON e.id = a.event_id AND e.fixture_id = a.fixture_id
    WHERE e.id IS NULL;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % video_assets rows disagree with their event fixture', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM video_assets a
    JOIN video_assets successor ON successor.id = a.superseded_by
    WHERE successor.event_id <> a.event_id OR successor.fixture_id <> a.fixture_id;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % video_assets rows supersede across event/fixture identity', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM video_shares s
    LEFT JOIN video_assets a ON a.id = s.asset_id AND a.event_id = s.event_id
    WHERE a.id IS NULL;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % video_shares rows disagree with their asset event', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM event_search_candidates c
    LEFT JOIN events e ON e.id = c.event_id AND e.fixture_id = c.fixture_id
    WHERE e.id IS NULL;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % candidate rows disagree with their event fixture', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM event_search_candidates c
    JOIN video_assets a ON a.id = c.credited_asset_id
    WHERE a.event_id <> c.event_id OR a.fixture_id <> c.fixture_id;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % candidate rows credit an asset from another event/fixture', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM events
    WHERE NOT (
        (removed = FALSE AND removed_reason IS NULL AND removed_at IS NULL) OR
        (removed = TRUE AND removed_reason IS NOT NULL AND removed_at IS NOT NULL)
    );
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % events rows violate removed-state completeness', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM video_shares
    WHERE NOT (
        (state IN ('active', 'superseded') AND removed_reason IS NULL AND removed_at IS NULL) OR
        (state = 'removed' AND removed_reason IS NOT NULL AND removed_at IS NOT NULL)
    );
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % video_shares rows violate removed-state completeness', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM video_assets
    WHERE octet_length(md5) <> 16
       OR octet_length(frame_hashes) < 8
       OR octet_length(frame_hashes) % 8 <> 0
       OR width <= 0 OR height <= 0 OR duration_ms <= 0
       OR file_size_bytes <= 0
       OR (bitrate IS NOT NULL AND bitrate <= 0)
       OR popularity < 1
       OR superseded_by = id;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % video_assets rows violate media/popularity bounds', invalid_count;
    END IF;

    SELECT count(*) INTO invalid_count
    FROM event_search_candidates
    WHERE duration_seconds < 0
       OR age_minutes_at_discovery < 0;
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'FF-071 preflight: % candidate rows contain negative duration/age values', invalid_count;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'events_identity_unique') THEN
        ALTER TABLE events
            ADD CONSTRAINT events_identity_unique UNIQUE (id, fixture_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_identity_event_unique') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_identity_event_unique UNIQUE (id, event_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_identity_event_fixture_unique') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_identity_event_fixture_unique UNIQUE (id, event_id, fixture_id);
    END IF;
END $$;

-- Replace independent existence checks with correlated identity constraints.
ALTER TABLE video_assets
    DROP CONSTRAINT IF EXISTS video_assets_event_id_fkey,
    DROP CONSTRAINT IF EXISTS video_assets_fixture_id_fkey,
    DROP CONSTRAINT IF EXISTS video_assets_superseded_by_fkey;
ALTER TABLE video_shares
    DROP CONSTRAINT IF EXISTS video_shares_asset_id_fkey,
    DROP CONSTRAINT IF EXISTS video_shares_event_id_fkey;
ALTER TABLE event_search_candidates
    DROP CONSTRAINT IF EXISTS event_search_candidates_event_id_fkey,
    DROP CONSTRAINT IF EXISTS event_search_candidates_fixture_id_fkey,
    DROP CONSTRAINT IF EXISTS event_search_candidates_credited_asset_id_fkey;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_event_fixture_fkey') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_event_fixture_fkey
            FOREIGN KEY (event_id, fixture_id)
            REFERENCES events (id, fixture_id) ON DELETE CASCADE NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_superseded_identity_fkey') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_superseded_identity_fkey
            FOREIGN KEY (superseded_by, event_id, fixture_id)
            REFERENCES video_assets (id, event_id, fixture_id)
            ON DELETE SET NULL (superseded_by) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_shares_asset_event_fkey') THEN
        ALTER TABLE video_shares
            ADD CONSTRAINT video_shares_asset_event_fkey
            FOREIGN KEY (asset_id, event_id)
            REFERENCES video_assets (id, event_id) ON DELETE RESTRICT NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'event_search_candidates_event_fixture_fkey') THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_event_fixture_fkey
            FOREIGN KEY (event_id, fixture_id)
            REFERENCES events (id, fixture_id) ON DELETE CASCADE NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'event_search_candidates_credited_identity_fkey') THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_credited_identity_fkey
            FOREIGN KEY (credited_asset_id, event_id, fixture_id)
            REFERENCES video_assets (id, event_id, fixture_id) ON DELETE RESTRICT NOT VALID;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'events_removed_state') THEN
        ALTER TABLE events ADD CONSTRAINT events_removed_state CHECK (
            (removed = FALSE AND removed_reason IS NULL AND removed_at IS NULL) OR
            (removed = TRUE AND removed_reason IS NOT NULL AND removed_at IS NOT NULL)
        ) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_shares_removed_state') THEN
        ALTER TABLE video_shares ADD CONSTRAINT video_shares_removed_state CHECK (
            (state IN ('active', 'superseded') AND removed_reason IS NULL AND removed_at IS NULL) OR
            (state = 'removed' AND removed_reason IS NOT NULL AND removed_at IS NOT NULL)
        ) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_media_shape') THEN
        ALTER TABLE video_assets ADD CONSTRAINT video_assets_media_shape CHECK (
            octet_length(md5) = 16 AND
            octet_length(frame_hashes) >= 8 AND
            octet_length(frame_hashes) % 8 = 0 AND
            width > 0 AND height > 0 AND duration_ms > 0 AND
            file_size_bytes > 0 AND
            (bitrate IS NULL OR bitrate > 0)
        ) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_popularity_positive') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_popularity_positive CHECK (popularity >= 1) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'video_assets_supersession_not_self') THEN
        ALTER TABLE video_assets
            ADD CONSTRAINT video_assets_supersession_not_self
            CHECK (superseded_by IS NULL OR superseded_by <> id) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'event_search_candidates_duration_nonnegative') THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_duration_nonnegative
            CHECK (duration_seconds >= 0) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'event_search_candidates_age_nonnegative') THEN
        ALTER TABLE event_search_candidates
            ADD CONSTRAINT event_search_candidates_age_nonnegative
            CHECK (age_minutes_at_discovery IS NULL OR age_minutes_at_discovery >= 0) NOT VALID;
    END IF;
END $$;

ALTER TABLE video_assets VALIDATE CONSTRAINT video_assets_event_fixture_fkey;
ALTER TABLE video_assets VALIDATE CONSTRAINT video_assets_superseded_identity_fkey;
ALTER TABLE video_shares VALIDATE CONSTRAINT video_shares_asset_event_fkey;
ALTER TABLE event_search_candidates VALIDATE CONSTRAINT event_search_candidates_event_fixture_fkey;
ALTER TABLE event_search_candidates VALIDATE CONSTRAINT event_search_candidates_credited_identity_fkey;
ALTER TABLE events VALIDATE CONSTRAINT events_removed_state;
ALTER TABLE video_shares VALIDATE CONSTRAINT video_shares_removed_state;
ALTER TABLE video_assets VALIDATE CONSTRAINT video_assets_media_shape;
ALTER TABLE video_assets VALIDATE CONSTRAINT video_assets_popularity_positive;
ALTER TABLE video_assets VALIDATE CONSTRAINT video_assets_supersession_not_self;
ALTER TABLE event_search_candidates VALIDATE CONSTRAINT event_search_candidates_duration_nonnegative;
ALTER TABLE event_search_candidates VALIDATE CONSTRAINT event_search_candidates_age_nonnegative;
