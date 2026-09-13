-- schema-hash: 5af0ceed55b94a7d8f24a04583f81b5c099bc67abdd6e6f0251e55d98fcbe978
-- Preserve reversible-selection topology and retry receipts without changing historical clips.
CREATE TABLE IF NOT EXISTS video_selection_commits (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL,
    fixture_id BIGINT NOT NULL,
    request_hash TEXT NOT NULL,
    snapshot_hash TEXT NOT NULL,
    policy JSONB NOT NULL,
    before_state JSONB NOT NULL,
    result JSONB NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_selection_commits_event_fkey
        FOREIGN KEY (event_id, fixture_id) REFERENCES events (id, fixture_id) ON DELETE CASCADE,
    CONSTRAINT video_selection_commits_record CHECK ((
        request_hash ~ '^[0-9a-f]{64}$' AND snapshot_hash ~ '^[0-9a-f]{64}$'
        AND jsonb_typeof(policy) = 'object'
        AND jsonb_typeof(before_state) = 'array'
        AND jsonb_typeof(result) = 'object'
        AND result#>>'{Plan,Version}' = 'direct-restoration-v1'
        AND octet_length(before_state::text) <= 1048576
        AND octet_length(result::text) <= 1048576
    ) IS TRUE)
);
CREATE INDEX IF NOT EXISTS video_selection_commits_event ON video_selection_commits(event_id,committed_at,id);
