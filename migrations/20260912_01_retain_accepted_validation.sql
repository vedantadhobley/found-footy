-- schema-hash: db5d21e1c33b62a7ece9e5f354971f6f214cf9a89856ed62c9b21c1ed3c11b9d
-- Retain acknowledged accepted evaluations once, attached to their exact asset.
CREATE TABLE IF NOT EXISTS video_asset_validations (
    id UUID PRIMARY KEY,
    asset_id UUID NOT NULL,
    event_id UUID NOT NULL,
    fixture_id BIGINT NOT NULL,
    evidence JSONB NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_asset_validations_asset_fkey
        FOREIGN KEY (asset_id, event_id, fixture_id)
        REFERENCES video_assets (id, event_id, fixture_id) ON DELETE CASCADE,
    CONSTRAINT video_asset_validations_evidence CHECK ((
        jsonb_typeof(evidence) = 'object'
        AND octet_length(evidence::text) <= 16384
        AND evidence->>'id' = id::text
        AND evidence->>'event_id' = event_id::text
        AND evidence->>'fixture_id' = fixture_id::text
        AND evidence->'version' = '1'::jsonb
        AND jsonb_typeof(evidence->'frames') = 'array'
        AND jsonb_array_length(evidence->'frames') BETWEEN 1 AND 3
        AND evidence#>>'{evaluation,Outcome}' IN ('verified', 'unverified')
    ) IS TRUE)
);
CREATE INDEX IF NOT EXISTS video_asset_validations_asset ON video_asset_validations (asset_id, recorded_at, id);
