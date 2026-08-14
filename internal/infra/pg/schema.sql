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
-- Derived from docs/design/rebuild-plan.md §3. This file is authoritative from
-- S2.2 onward; if the two diverge, this file wins and rebuild-plan.md
-- gets updated.

-- ────────────────────────────────────────────────────────────────
-- Extensions
-- ────────────────────────────────────────────────────────────────

CREATE EXTENSION IF NOT EXISTS pgcrypto;                    -- gen_random_uuid()
-- Note: pg_trgm was declared for a team_name fuzzy-match GIN index in
-- an earlier team_aliases shape. The new deterministic pipeline looks
-- up teams by exact team_id (API-Football), so trigram matching isn't
-- needed. Re-add if a future feature needs fuzzy string search.

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
CREATE TYPE share_state AS ENUM ('active', 'removed', 'superseded');

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
    -- Vendor display facts for the portal's competition line (api league.*).
    league_country TEXT NOT NULL DEFAULT '',                -- 'World', 'USA', 'England'
    league_round TEXT NOT NULL DEFAULT '',                  -- 'Group Stage - 1', 'Regular Season - 12'
    home_score INT,
    away_score INT,
    -- Penalty shootout result (api score.penalty). Nullable — non-null only on
    -- a shootout; the rest of the score breakdown (HT/FT/ET) stays dropped.
    home_penalty INT,
    away_penalty INT,
    -- Winner data (populated from api teams.home.winner / away.winner).
    -- Nullable BOOLEAN — vendor sets true/false when result is decided
    -- (usually simultaneously with terminal status, sometimes slightly
    -- earlier). Fixture completion has a fast-path on either being
    -- non-null (skips the 3-poll completion counter).
    home_winner BOOLEAN,
    away_winner BOOLEAN,

    -- Our enhancement fields
    activated_at TIMESTAMPTZ,                               -- when we moved to 'active'
    completed_at TIMESTAMPTZ,                               -- when we moved to 'completed'
    last_activity_at TIMESTAMPTZ,                           -- for frontend sort ordering
    last_polled_at TIMESTAMPTZ,                             -- most recent monitor cycle

    -- Completion counter — 3-poll debounce on APIStatus.Terminal().
    -- Increments (cap 3) each ActivePoll cycle where status is Terminal;
    -- resets to 0 on any non-Terminal observation. See
    -- docs/design/proposals/completion-contract.md.
    completion_counter INT NOT NULL DEFAULT 0
        CHECK (completion_counter >= 0 AND completion_counter <= 3),

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

-- 6. event_downstream_workflows — the pluggable fixture-completion
-- checklist. Every downstream workflow (Discovery, DownloadWorkflow-N,
-- UploadWorkflow, future sentiment analysis, etc.) registers a row at
-- START with completed_at=NULL and UPDATEs completed_at when it exits.
--
-- Fixture completion asks: any rows for any event in this fixture where
-- completed_at IS NULL? If yes, fixture stays active (downstream still
-- writing). If no, fixture is a candidate to complete (given API status
-- Terminal + counter satisfied + all events debounce-settled).
--
-- Pluggability: adding a new downstream workflow type requires ZERO
-- schema change — just pick a new workflow_type string. See
-- docs/design/proposals/completion-contract.md.
--
-- Coexistence note: event_download_workflows (above) tracks the 10-
-- download registration threshold specifically. As of the 2026-07-11
-- completion contract, new downstream workflows should register here
-- instead — event_download_workflows may be consolidated into this
-- table when O4 lands.
CREATE TABLE event_downstream_workflows (
    event_id      UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    workflow_type TEXT NOT NULL,                             -- 'discovery', 'download', 'upload', 'sentiment', ...
    workflow_id   TEXT NOT NULL,                             -- Temporal workflow ID
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at  TIMESTAMPTZ,                               -- NULL = still in flight
    outcome_class TEXT,                                      -- 'success', 'failed_geo_restricted', 'timeout', ...
    metadata      JSONB,                                     -- workflow-type-specific extras
    PRIMARY KEY (event_id, workflow_type, workflow_id)
);

-- Partial index optimized for the "any in-flight?" completion check.
CREATE INDEX event_downstream_workflows_pending
    ON event_downstream_workflows (event_id)
    WHERE completed_at IS NULL;

-- 7a. video_assets — canonical byte-store, one row per unique CLIP per EVENT.
--    Dedup is scoped to the event and NEVER across events. Cross-event / per-fixture
--    dedup is dead: tried in Python, rejected — it collapsed genuinely-distinct goals
--    (visually-similar broadcast clips: same stadium, camera, celebration) into one and
--    dropped legitimate videos. Cross-event clip-bleed is instead handled by timestamp
--    extraction (the clock check rejects a clip whose broadcast minute doesn't match the
--    event's reported minute). See decisions.md 2026-07-25.
--
--    DEDUP LIVES IN WORKFLOW CODE, NOT HERE (2026-08-03, #166). Perceptual dedup is a
--    FUZZY sliding-window match over the per-frame hash SEQUENCE (offset/gap-tolerant) —
--    which no SQL constraint can express. The EventWorkflow consumer decides dedup
--    in-memory BEFORE insert, so the DB only enforces what it honestly can: exact-byte
--    uniqueness via UNIQUE (event_id, md5). frame_hashes is stored as a queryable RECORD
--    (debugging / re-dedup / analysis), NOT as a decision-maker. The old single
--    perceptual_hash + UNIQUE (event_id, perceptual_hash) + LSH prefix scheme is gone.
CREATE TABLE video_assets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- event_id is the dedup scope; fixture_id is a denormalized convenience for the
    -- s3 path + prune queries (an event never changes fixtures, so they can't disagree).
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,

    -- Storage
    s3_bucket TEXT NOT NULL,
    s3_key TEXT NOT NULL,                                   -- computed from (fixture_id, id); the uuid id guarantees uniqueness

    -- Content identity
    md5 BYTEA NOT NULL,                                     -- 16-byte whole-file digest — the exact-dup layer
    frame_hashes BYTEA NOT NULL,                            -- per-frame dHash sequence: 8 bytes (big-endian uint64) per 0.1s frame; count = octet_length/8. Record only — dedup is in workflow code.

    -- Metadata
    width INT NOT NULL,
    height INT NOT NULL,
    duration_ms INT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    bitrate INT,
    aspect_ratio REAL GENERATED ALWAYS AS (width::REAL / height::REAL) STORED,

    -- Popularity (within-event vote count — how many of this event's candidates deduped onto this asset)
    popularity INT NOT NULL DEFAULT 1,

    -- Supersession (dedup-merge / re-encode / higher-quality replacement — within one event)
    superseded_by UUID REFERENCES video_assets(id) ON DELETE SET NULL,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (event_id, md5)                                  -- exact-byte dedup + insert idempotency within an event
);

CREATE INDEX video_assets_event_popularity ON video_assets (event_id, popularity DESC)
    WHERE superseded_by IS NULL;

-- 7b. video_shares — public share IDs, per-event ranked
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

    -- 'superseded' = replaced by a higher-quality/consolidated clip; not a
    -- removal (no reason), still resolvable for direct-URL play, just not listed.
    CHECK ((state IN ('active', 'superseded') AND removed_reason IS NULL) OR (state = 'removed' AND removed_reason IS NOT NULL))
);

-- The partial UNIQUE INDEX on (event_id, rank) WHERE state='active' is the
-- fix for the 2026-06-30 rank-drift bug (ranks 0, 0, 2, 3 on Norway-CIV).
-- Postgres will reject any attempt to write duplicate active ranks per event.
CREATE UNIQUE INDEX video_shares_event_rank_active
    ON video_shares (event_id, rank)
    WHERE state = 'active';

CREATE INDEX video_shares_event ON video_shares (event_id);
CREATE INDEX video_shares_asset ON video_shares (asset_id);

-- 7b. event_search_candidates — Discovery workflow's raw candidate log.
--
-- Every video-carrying tweet DiscoveryWorkflow surfaces via a
-- /search call gets one row here — accepted or later rejected, we
-- want the audit trail. Enables post-hoc learning ("did our query
-- surface the tweet with the good goal video? was it discarded by
-- V-phase LLM validation or dedup?") without re-running historical
-- searches (which don't reliably reproduce past-window results, per
-- decisions.md 2026-07-22).
--
-- Per twitter-search-query.md D5 + 2026-07-23 sign-off: Discovery
-- runs a fixed number of attempts (config DISCOVERY_MAX_ATTEMPTS,
-- default 15) × 60 s spacing. search_attempt is the attempt number
-- (1..N; the CHECK below bounds it at 20 as a sanity ceiling) that
-- produced this candidate — attempts 2+
-- typically insert very few new rows because our T/c consecutive-
-- already-seen scroll stop terminates the search early once we hit
-- the exclude_urls tail.
--
-- Uniqueness: (event_id, tweet_url) — the same tweet can't be
-- re-inserted for the same event across attempts. Downstream V-phase
-- adds decision fields (rejection_reason, decision_class) via ALTER
-- when it ships; kept out of the initial DDL to avoid speculating on
-- shape before we have empirical rejection cases.
CREATE TABLE event_search_candidates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,

    -- Search context — which attempt surfaced this candidate.
    search_attempt INT NOT NULL CHECK (search_attempt BETWEEN 1 AND 20),
    query TEXT NOT NULL,                                    -- the exact query string; useful for observability + query-tuning audits

    -- Candidate tweet payload (from Twitter service's VideoRef).
    tweet_url TEXT NOT NULL,
    tweet_text TEXT NOT NULL DEFAULT '',
    video_page_url TEXT NOT NULL,
    duration_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    username TEXT NOT NULL DEFAULT '',
    age_minutes_at_discovery DOUBLE PRECISION,              -- extracted from time[datetime] at scrape time; NULL if Twitter didn't render it

    discovered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (event_id, tweet_url)                            -- same tweet can't re-insert across attempts
);

CREATE INDEX event_search_candidates_event
    ON event_search_candidates (event_id);
CREATE INDEX event_search_candidates_fixture
    ON event_search_candidates (fixture_id);
CREATE INDEX event_search_candidates_discovered_at
    ON event_search_candidates (discovered_at);

-- 9. team_aliases — deterministic Wikidata alias cache.
--
-- One row per API-Football team. Two-phase population:
--
--   Phase 1 (Ingest): placeholder row inserted when we first see a team
--   (canonical_name + team_code + country + city + is_national from
--   the vendor). wikidata_qid and aliases stay NULL/empty until the
--   resolution activity runs.
--
--   Phase 2 (alias resolution): the deterministic Wikidata pipeline
--   populates wikidata_qid + aliases + resolved_at. wikidata_qid is
--   cached permanently — QIDs are stable, so once resolved a team is a
--   permanent cache-hit: the expensive Wikipedia CirrusSearch lookup +
--   selection never re-run (resolve-once; there is NO 30-day TTL — the
--   IsFresh check is dead code, see team-aliases.md).
--
-- Placeholder rows have resolved_at IS NULL (unresolved); a set
-- wikidata_qid means resolved + skipped forever. Design ref:
-- docs/design/proposals/team-aliases.md.
CREATE TABLE team_aliases (
    team_id INT PRIMARY KEY,                                -- API-Football team ID
    canonical_name TEXT NOT NULL,                           -- API-Football team.name at ingest time
    team_code TEXT,                                         -- API-Football team.code (3-letter FIFA/UEFA)
    country TEXT,                                           -- API-Football team.country
    city TEXT,                                              -- API-Football venue.city
    is_national BOOLEAN NOT NULL,                           -- API-Football team.national
    wikidata_qid TEXT,                                      -- NULL until first resolution; permanent thereafter
    aliases TEXT[] NOT NULL DEFAULT '{}',                   -- normalized lowercase words for Twitter OR-query
    resolved_at TIMESTAMPTZ,                                -- NULL = placeholder; set on successful resolution
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Refresh scan predicate: placeholders (resolved_at IS NULL) OR stale
-- (resolved_at < now() - '30 days'). Partial index keeps the scan cheap
-- even after every tracked team has been resolved.
CREATE INDEX team_aliases_needs_refresh
    ON team_aliases (resolved_at NULLS FIRST);

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
