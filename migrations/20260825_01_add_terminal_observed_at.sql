-- schema-hash: b479df7bb3567870ec0ec4320c37f52821cb738bf2358f40b6e897ac52af1447
-- Adds the durable terminal-grace anchor while retaining prior-binary SQL compatibility.

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
