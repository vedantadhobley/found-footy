-- schema-hash: 8a9ba0bebf596112f1032bc5b9a892f64bec9e1abc8cebd1ed9b1bb9e0ac12e2
-- Preserve committed provider-reconciliation effects across activity retries.
CREATE TABLE IF NOT EXISTS provider_mutation_receipts (
    observation_id TEXT NOT NULL CHECK (length(observation_id) BETWEEN 1 AND 200),
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    health_generation BIGINT NOT NULL CHECK (health_generation >= 0),
    result JSONB NOT NULL,
    PRIMARY KEY (observation_id, fixture_id),
    CONSTRAINT provider_mutation_receipt_result_check CHECK ((
        jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 1048576
    ) IS TRUE)
);
CREATE INDEX IF NOT EXISTS provider_mutation_receipts_observed ON provider_mutation_receipts(observed_at);
