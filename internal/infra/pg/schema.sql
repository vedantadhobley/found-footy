-- found-footy initial schema
--
-- Source of truth for the Postgres schema. Applied by:
--   1. docker-compose.dev.yml: mounted into /docker-entrypoint-initdb.d/
--      so dev postgres provisions the schema on first startup (fresh
--      volume only — Postgres skips initdb.d when data dir is populated).
--   2. internal/infra/pg tests: passed to tcpostgres.WithInitScripts()
--      when spinning up ephemeral test containers.
--   3. Future migration tooling: schema.go embeds this file via
--      //go:embed so it can be applied programmatically.
--
-- Derived from docs/rebuild-plan.md §3. This file is authoritative from
-- S2.2 onward; if the two diverge, this file wins and rebuild-plan.md
-- gets updated.

-- ────────────────────────────────────────────────────────────────
-- Extensions
-- ────────────────────────────────────────────────────────────────

CREATE EXTENSION IF NOT EXISTS pgcrypto;                    -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pg_trgm;                     -- fuzzy team-name matching
CREATE EXTENSION IF NOT EXISTS vector;                      -- pgvector for embeddings

-- ────────────────────────────────────────────────────────────────
-- Enums
-- ────────────────────────────────────────────────────────────────

-- Fixture lifecycle phase (our concept, derived from API status + our decisions)
CREATE TYPE fixture_state AS ENUM ('staging', 'active', 'completed');

-- Event type (API-Football's classification, title-cased)
-- API-Football uses lowercase 'subst'; ingest-side normalization title-cases
-- to match this enum before INSERT.
-- event_type is the DOMAIN classification (not raw vendor Type). Includes
-- `missed penalty` as a distinct domain type — the vendor sends this under
-- Type=Goal with Detail=Missed Penalty, but we classify it separately so
-- the UI can display "saved penalty" moments differently from goals.
-- Subst / Var currently parse but aren't stored (see TrackableEventType).
--
-- Casing policy (decisions.md 2026-07-09 lowercase-canonical entry):
-- ALL enum values across vendor + domain use lowercase, preserving
-- vendor's word separators (spaces) for multi-word values. Parse
-- normalizes at wire boundary so vendor's casing inconsistencies
-- (e.g., "Red Card" vs "Red card") don't propagate.
CREATE TYPE event_type AS ENUM ('goal', 'card', 'subst', 'var', 'missed penalty');

-- Video share state
CREATE TYPE share_state AS ENUM ('active', 'removed');

-- Tweet source classification (from semantic intent, §1 extensibility hook)
CREATE TYPE source_type AS ENUM (
    'broadcaster',      -- official broadcaster account (BBC Sport, ESPN, etc.)
    'media_outlet',     -- media / journalist account
    'verified_fan',     -- verified account, fan-oriented
    'unverified'        -- random user
);

-- Removal reason for shares / events (why did we mark this removed)
CREATE TYPE removal_reason AS ENUM (
    'var',              -- VAR reversed the goal
    'policy',           -- manual policy decision
    'asset_gone'        -- underlying asset deleted (should be rare)
);

-- ────────────────────────────────────────────────────────────────
-- Trigger function
-- ────────────────────────────────────────────────────────────────

-- Auto-updating updated_at trigger shared by every table that carries an
-- updated_at column. Without this, updated_at DEFAULTs to NOW() at INSERT
-- but never advances on UPDATE (Postgres does not update a DEFAULT'd
-- column on UPDATE the way MySQL's ON UPDATE clause would).
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END $$ LANGUAGE plpgsql;

-- ────────────────────────────────────────────────────────────────
-- Tables (in dependency order)
-- ────────────────────────────────────────────────────────────────

-- 1. fixtures — one row per match, lifecycle state in a column
CREATE TABLE fixtures (
    id BIGINT PRIMARY KEY,                                  -- API-Football fixture ID
    state fixture_state NOT NULL DEFAULT 'staging',

    -- API-reported (refreshed on each monitor poll for state='active')
    api_status_short TEXT NOT NULL,                         -- 'NS', '1H', 'FT', etc.
    api_status_long TEXT NOT NULL,
    api_elapsed INT,                                        -- match minute (nullable pre-kickoff)
    api_extra INT,                                          -- stoppage time
    kickoff TIMESTAMPTZ NOT NULL,
    home_team_id INT NOT NULL,
    home_team_name TEXT NOT NULL,
    away_team_id INT NOT NULL,
    away_team_name TEXT NOT NULL,
    league_id INT NOT NULL,
    league_name TEXT NOT NULL,
    league_season INT NOT NULL,
    home_score INT,
    away_score INT,

    -- Our enhancement fields
    activated_at TIMESTAMPTZ,                               -- when we moved to 'active'
    completed_at TIMESTAMPTZ,                               -- when we moved to 'completed'
    last_activity_at TIMESTAMPTZ,                           -- for frontend sort ordering
    last_polled_at TIMESTAMPTZ,                             -- most recent monitor cycle

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (
        (state = 'staging' AND activated_at IS NULL AND completed_at IS NULL) OR
        (state = 'active' AND activated_at IS NOT NULL AND completed_at IS NULL) OR
        (state = 'completed' AND activated_at IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX fixtures_staging_by_kickoff ON fixtures (kickoff) WHERE state = 'staging';
CREATE INDEX fixtures_active_by_polled ON fixtures (last_polled_at) WHERE state = 'active';
CREATE INDEX fixtures_completed_recent ON fixtures (completed_at DESC) WHERE state = 'completed';

CREATE TRIGGER trg_fixtures_updated_at
    BEFORE UPDATE ON fixtures
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 2. events — one row per API-reported event, per-fixture unique on natural key
CREATE TABLE events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,

    -- Natural key: unique per fixture, human-readable
    natural_key TEXT NOT NULL,                              -- '{team_id}_{player_id}_{type}_{seq}'

    -- API-reported
    event_type event_type NOT NULL,
    detail TEXT NOT NULL,                                   -- 'Normal Goal', 'Yellow Card', etc.
    team_id INT NOT NULL,
    team_name TEXT NOT NULL,
    player_id INT,                                          -- nullable: API sometimes reports goals with unknown player
    player_name TEXT,
    minute INT NOT NULL,
    extra INT,

    -- Our enhancement fields
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Symmetric debounce counter (0..3). Insert seeds it to 1.
    -- Presence votes increment (cap 3). Absence votes decrement (floor 0).
    -- On first crossing of 3: downstream_triggered flips TRUE (one-way).
    -- On hitting 0: event auto-removed (soft-delete) — see removed below.
    debounce_count INT NOT NULL DEFAULT 1
        CHECK (debounce_count BETWEEN 0 AND 3),
    downstream_triggered BOOLEAN NOT NULL DEFAULT FALSE,
    -- Legacy monitor_complete kept for backward compat during transition;
    -- downstream_triggered is the authoritative signal going forward.
    monitor_complete BOOLEAN NOT NULL DEFAULT FALSE,
    download_complete BOOLEAN NOT NULL DEFAULT FALSE,       -- 10 download attempts fired
    removed BOOLEAN NOT NULL DEFAULT FALSE,
    removed_reason removal_reason,
    removed_at TIMESTAMPTZ,

    -- Telemetry (Phase 1 from audit) — flexible JSONB for evolving structure
    telemetry JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (fixture_id, natural_key),                       -- prevents duplicate detection races
    CHECK ((removed = FALSE AND removed_reason IS NULL) OR (removed = TRUE AND removed_reason IS NOT NULL))
);

CREATE INDEX events_fixture ON events (fixture_id);
CREATE INDEX events_pending_work ON events (fixture_id)
    WHERE NOT removed AND (NOT monitor_complete OR NOT download_complete);
CREATE INDEX events_by_first_seen ON events (first_seen_at DESC);

CREATE TRIGGER trg_events_updated_at
    BEFORE UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 3. event_monitor_workflows — 3-poll debounce tracking, idempotent
CREATE TABLE event_monitor_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, workflow_id)
);

-- 4. event_download_workflows — 10-download completion tracking with typed failure class
CREATE TABLE event_download_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    outcome_class TEXT,                                     -- typed error class if failed, NULL if succeeded
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (event_id, workflow_id)
);

-- 5. event_drop_workflows — 3-drop VAR-detection tracking
CREATE TABLE event_drop_workflows (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (event_id, workflow_id)
);

-- 6. video_assets — canonical byte-store, one row per unique perceptual hash per fixture
CREATE TABLE video_assets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,

    -- Storage
    s3_bucket TEXT NOT NULL,
    s3_key TEXT NOT NULL,                                   -- computed from (fixture_id, id), not concatenated at write

    -- Content identity
    perceptual_hash BYTEA NOT NULL,                         -- dHash as raw 8 bytes for fast Hamming
    perceptual_hash_prefix INT NOT NULL,                    -- first 16 bits, indexable for LSH-style bucket lookup
    md5 BYTEA NOT NULL,

    -- Metadata
    width INT NOT NULL,
    height INT NOT NULL,
    duration_ms INT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    bitrate INT,
    aspect_ratio REAL GENERATED ALWAYS AS (width::REAL / height::REAL) STORED,

    -- Popularity (cross-event vote count)
    popularity INT NOT NULL DEFAULT 1,

    -- Supersession (dedup-merge / re-encode / higher-quality replacement)
    superseded_by UUID REFERENCES video_assets(id) ON DELETE SET NULL,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (fixture_id, md5),                               -- exact-byte dedup within a fixture
    UNIQUE (fixture_id, perceptual_hash)                    -- perceptual dedup — makes audit §4 atomic INSERT work
);

CREATE INDEX video_assets_hash_prefix ON video_assets (fixture_id, perceptual_hash_prefix)
    WHERE superseded_by IS NULL;
CREATE INDEX video_assets_fixture_popularity ON video_assets (fixture_id, popularity DESC)
    WHERE superseded_by IS NULL;

-- 7. video_shares — public share IDs, per-event ranked
CREATE TABLE video_shares (
    id TEXT PRIMARY KEY,                                    -- 's_<12-hex>', public
    asset_id UUID NOT NULL REFERENCES video_assets(id) ON DELETE RESTRICT,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE RESTRICT,

    -- Validation snapshot at share creation time
    timestamp_verified BOOLEAN NOT NULL,
    extracted_minute INT,

    -- State
    state share_state NOT NULL DEFAULT 'active',
    removed_reason removal_reason,
    removed_at TIMESTAMPTZ,

    -- Ranking — 1-indexed, unique per event within active state
    rank INT NOT NULL CHECK (rank >= 1),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK ((state = 'active' AND removed_reason IS NULL) OR (state = 'removed' AND removed_reason IS NOT NULL))
);

-- The partial UNIQUE INDEX on (event_id, rank) WHERE state='active' is the
-- fix for the 2026-06-30 rank-drift bug (ranks 0, 0, 2, 3 on Norway-CIV).
-- Postgres will reject any attempt to write duplicate active ranks per event.
CREATE UNIQUE INDEX video_shares_event_rank_active
    ON video_shares (event_id, rank)
    WHERE state = 'active';

CREATE INDEX video_shares_event ON video_shares (event_id);
CREATE INDEX video_shares_asset ON video_shares (asset_id);

-- 8. tweet_intent — semantic intent extraction (§1 extensibility hook, wired from day one)
CREATE TABLE tweet_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    video_asset_id UUID NOT NULL REFERENCES video_assets(id) ON DELETE CASCADE,

    -- Source metadata (extracted from Twitter response, not LLM-classified)
    tweet_url TEXT NOT NULL,
    author_handle TEXT NOT NULL,
    author_verified BOOLEAN NOT NULL DEFAULT FALSE,

    -- LLM classification
    source_type source_type NOT NULL,
    event_type_mentioned event_type,
    confidence REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    urgency REAL CHECK (urgency BETWEEN 0 AND 1),

    -- Embedding for similarity clustering.
    -- 768 assumes current Qwen3-Embedding-8B on joi; migration + backfill
    -- needed if the deployed embedding model changes.
    embedding vector(768),

    -- Raw text for auditing
    tweet_text TEXT NOT NULL,

    llm_model TEXT NOT NULL,                                -- which model classified
    analyzed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (video_asset_id)                                 -- one intent per asset; re-analysis updates
);

CREATE INDEX tweet_intent_source ON tweet_intent (source_type);
CREATE INDEX tweet_intent_embedding ON tweet_intent
    USING hnsw (embedding vector_cosine_ops);

-- 9. team_aliases — RAG cache
CREATE TABLE team_aliases (
    team_id INT PRIMARY KEY,                                -- API-Football team ID
    team_name TEXT NOT NULL,                                -- original name, for display
    is_national BOOLEAN NOT NULL,
    country TEXT,
    city TEXT,
    wikidata_qid TEXT,
    wikidata_aliases TEXT[] NOT NULL DEFAULT '{}',          -- raw Wikidata pipeline output (audit trail)
    twitter_aliases TEXT[] NOT NULL DEFAULT '{}',           -- LLM-selected, normalized (diacritics stripped)
    llm_model TEXT,                                         -- which model generated twitter_aliases
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX team_aliases_name_trgm ON team_aliases USING gin (team_name gin_trgm_ops);

CREATE TRIGGER trg_team_aliases_updated_at
    BEFORE UPDATE ON team_aliases
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 9b. tracked_teams_cache — dynamic top-flight team list for ingest filter.
-- IngestWorkflow's step 0 refreshes this every 24h (default) by calling
-- /leagues?id=X for each TRACKED_LEAGUES entry to get current season, then
-- /teams?league=X&season=Y to get every team in that league. All those
-- team IDs land here; the fetch step filters returned fixtures by
-- (home_team_id OR away_team_id) ∈ this set.
--
-- Mirrors Python's `get_top_flight_team_ids` (archive/src/utils/team_data.py)
-- with pg replacing Mongo. Season rollover + promotion/relegation are handled
-- automatically because the refresh re-fetches every 24h. See decisions.md
-- 2026-07-09 Ingest-regression-fix entry for the design rationale.
CREATE TABLE tracked_teams_cache (
    team_id INT PRIMARY KEY,                                -- API-Football team ID
    team_name TEXT NOT NULL,                                -- for observability + debugging
    league_id INT NOT NULL,                                 -- which league's roster this team came from
    league_name TEXT NOT NULL,                              -- denormalized for debug logs
    season INT NOT NULL,                                    -- season year (2026 = 2026-27 for European leagues)
    refreshed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()         -- ingest workflow reads to decide refresh vs cache-hit
);

CREATE INDEX tracked_teams_cache_league_season ON tracked_teams_cache (league_id, season);
CREATE INDEX tracked_teams_cache_refreshed_at ON tracked_teams_cache (refreshed_at);

-- 10. twitter_sessions — cookie coordination, single-row canonical pattern
CREATE TABLE twitter_sessions (
    id TEXT PRIMARY KEY,                                    -- 'canonical'
    cookies BYTEA NOT NULL,                                 -- serialized cookie blob
    cookies_version BIGINT NOT NULL DEFAULT 1,              -- monotonic; bumped on each re-auth
    authenticated BOOLEAN NOT NULL DEFAULT FALSE,
    last_refresh_at TIMESTAMPTZ,
    last_search_succeeded_at TIMESTAMPTZ,
    consecutive_auth_failures INT NOT NULL DEFAULT 0,
    estimated_expiry_at TIMESTAMPTZ,
    reauth_notes TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER trg_twitter_sessions_updated_at
    BEFORE UPDATE ON twitter_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- 11. event_log — durable audit trail + SSE-reconnect backfill source
CREATE TABLE event_log (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,                               -- 'event.detected', 'event.video_ready', 'fixture.completed', ...
    fixture_id BIGINT,
    event_id UUID,
    video_share_id TEXT,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX event_log_created ON event_log (created_at DESC);
CREATE INDEX event_log_event ON event_log (event_id) WHERE event_id IS NOT NULL;

-- Retention: partition by day (pg_partman when it lands; manual DELETE on
-- cron until then), drop partitions after 30 days. 30-day window is a hard
-- cap on SSE-reconnect backfill; consumers offline longer must re-hydrate
-- via /api/v1/fixtures + /api/v1/events (see §8).

-- 12. webhook_subscriptions + webhook_deliveries (§8 webhook delivery)
CREATE TABLE webhook_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    consumer_name TEXT NOT NULL,                            -- 'vedanta-systems', 'og-server', etc.
    url TEXT NOT NULL,
    event_types TEXT[] NOT NULL DEFAULT '{}',               -- empty = all
    hmac_secret TEXT NOT NULL,                              -- for X-FF-Signature
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (consumer_name, url)
);

CREATE TRIGGER trg_webhook_subs_updated_at
    BEFORE UPDATE ON webhook_subscriptions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_log_id BIGINT NOT NULL REFERENCES event_log(id) ON DELETE CASCADE,
    attempt_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),          -- when the delivery attempt was first recorded
    last_attempt_at TIMESTAMPTZ,
    last_response_code INT,
    last_response_body TEXT,
    succeeded_at TIMESTAMPTZ,
    give_up_at TIMESTAMPTZ,                                 -- set when max_attempts reached
    UNIQUE (subscription_id, event_log_id)                  -- one delivery record per (subscription, event)
);

CREATE INDEX webhook_deliveries_created ON webhook_deliveries (created_at DESC);

-- 13. outbox_cursor — self-heal cursor for the §9 NATS outbox catch-up worker.
-- Singleton row (CHECK ensures it) tracking the highest event_log.id that
-- has been republished to the NATS stream by the outbox worker on
-- drop-recovery.
CREATE TABLE outbox_cursor (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_published_event_log_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO outbox_cursor DEFAULT VALUES ON CONFLICT DO NOTHING;
