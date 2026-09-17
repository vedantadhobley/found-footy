-- schema-hash: 6393e0964045fed52ce0e466a666ee18fa27aba3c519d2407cfc69bb30a27756
-- Provider health is internal evidence, not public fixture/event state. Poll
-- receipts expire after 14 days; unresolved incident baselines do not expire.
CREATE TABLE IF NOT EXISTS provider_health (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_observed_at TIMESTAMPTZ,
    generation BIGINT NOT NULL DEFAULT 0 CHECK (generation >= 0),
    policy TEXT NOT NULL DEFAULT 'trusted' CHECK (policy IN ('trusted', 'positive_only')),
    evidence JSONB NOT NULL DEFAULT '{"Version":1}'::jsonb,
    CONSTRAINT provider_health_evidence_check CHECK ((
        jsonb_typeof(evidence) = 'object' AND evidence->>'Version' = '1'
        AND octet_length(evidence::text) <= 1048576
    ) IS TRUE)
);
INSERT INTO provider_health DEFAULT VALUES ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS provider_fixture_health (
    fixture_id BIGINT PRIMARY KEY REFERENCES fixtures(id) ON DELETE CASCADE,
    last_poll_id TEXT NOT NULL CHECK (length(last_poll_id) BETWEEN 1 AND 200),
    last_observed_at TIMESTAMPTZ NOT NULL,
    last_usable_at TIMESTAMPTZ,
    continuous_since TIMESTAMPTZ,
    clock_progress_at TIMESTAMPTZ,
    phase_since TIMESTAMPTZ,
    observed_status TEXT NOT NULL,
    clock_status TEXT NOT NULL,
    clock_elapsed INT CHECK (clock_elapsed >= 0),
    clock_extra INT CHECK (clock_extra >= 0),
    evidence JSONB NOT NULL,
    CONSTRAINT provider_fixture_health_evidence_check CHECK ((
        jsonb_typeof(evidence) = 'object' AND evidence->>'Version' = '1'
        AND evidence->>'Availability' IN ('usable', 'transport', 'contract', 'unobserved')
        AND evidence->>'Freshness' IN ('unknown', 'within_window', 'not_clocked', 'stalled', 'boundary_warning')
        AND jsonb_array_length(evidence->'Incidents') <= 10
        AND octet_length(evidence::text) <= 1048576
    ) IS TRUE),
    CONSTRAINT provider_fixture_health_times_check CHECK (
        (last_usable_at IS NULL OR last_usable_at <= last_observed_at)
        AND (continuous_since IS NULL OR (last_usable_at IS NOT NULL AND continuous_since <= last_usable_at))
        AND (clock_progress_at IS NULL OR (last_usable_at IS NOT NULL AND clock_progress_at <= last_usable_at))
        AND (phase_since IS NULL OR (last_usable_at IS NOT NULL AND phase_since <= last_usable_at))
    )
);

CREATE TABLE IF NOT EXISTS provider_health_polls (
    poll_id TEXT PRIMARY KEY CHECK (length(poll_id) BETWEEN 1 AND 200),
    observed_at TIMESTAMPTZ NOT NULL,
    failed_attempts JSONB NOT NULL DEFAULT '{}'::jsonb,
    result JSONB,
    CONSTRAINT provider_health_poll_evidence_check CHECK ((
        jsonb_typeof(failed_attempts) = 'object' AND octet_length(failed_attempts::text) <= 1048576
        AND (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 4194304))
    ) IS TRUE)
);
CREATE INDEX IF NOT EXISTS provider_health_polls_observed ON provider_health_polls(observed_at);
