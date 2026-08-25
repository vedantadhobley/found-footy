-- Adds the durable terminal-grace anchor while retaining prior-binary SQL compatibility.
BEGIN;

ALTER TABLE fixtures
    ADD COLUMN IF NOT EXISTS terminal_observed_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'fixtures'::regclass
          AND conname = 'fixtures_terminal_observation_state'
    ) THEN
        ALTER TABLE fixtures
            ADD CONSTRAINT fixtures_terminal_observation_state
            CHECK (terminal_observed_at IS NULL OR state IN ('active', 'completed'))
            NOT VALID;
    END IF;
END
$$;

ALTER TABLE fixtures
    VALIDATE CONSTRAINT fixtures_terminal_observation_state;

-- VerifySchema compares this stamp with schema.sql embedded in the next image.
UPDATE schema_version
SET schema_hash = 'b479df7bb3567870ec0ec4320c37f52821cb738bf2358f40b6e897ac52af1447', applied_at = NOW()
WHERE id = 1;

COMMIT;

-- Rollback note: the prior binary is SQL-compatible because this migration is
-- additive and retains completion_counter, but its drift guard knows only the
-- prior schema fingerprint. Before starting that image, deliberately restamp:
-- UPDATE schema_version
-- SET schema_hash='865995d13040e10857351a130d5eb088e4e857d4ffd1baf6f4515f1eb7ff631b', applied_at = NOW()
-- WHERE id = 1;
-- Do not drop terminal_observed_at during the rollback window.
