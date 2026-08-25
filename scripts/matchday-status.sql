-- Read-only match-day fixture, event, workflow, and candidate status report.
\set QUIET 1
\pset pager off
\pset null '—'
BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
\set QUIET 0

\echo 'Fixtures (recent or within lookahead)'
SELECT
    f.id,
    to_char(f.kickoff AT TIME ZONE 'America/New_York', 'YYYY-MM-DD HH24:MI') AS kickoff_et,
    f.state,
    f.api_status_short AS status,
    concat_ws(' ', f.api_elapsed, CASE WHEN f.api_extra > 0 THEN '+' || f.api_extra END) AS clock,
    f.home_team_name || ' vs ' || f.away_team_name AS fixture,
    coalesce(f.home_score::text, '—') || '-' || coalesce(f.away_score::text, '—') AS score,
    f.terminal_observed_at,
    CASE
        WHEN f.terminal_observed_at IS NOT NULL
        THEN now() - f.terminal_observed_at
    END AS terminal_age,
    (
        SELECT count(DISTINCT t.team_id)
        FROM tracked_teams_cache AS t
        WHERE t.team_id IN (f.home_team_id, f.away_team_id)
    ) AS tracked_sides,
    f.last_polled_at
FROM fixtures AS f
WHERE f.kickoff BETWEEN now() - interval '2 hours'
                    AND now() + make_interval(hours => :lookahead_hours)
   OR f.state = 'active'
ORDER BY f.kickoff, f.id;

\echo ''
\echo 'Event pipeline state for those fixtures'
SELECT
    e.fixture_id,
    e.id AS event_id,
    e.event_type,
    e.player_name,
    concat_ws('+', e.minute, e.extra) AS minute,
    e.debounce_count AS debounce,
    e.downstream_triggered AS triggered,
    e.removed,
    coalesce(dw.running, 0) AS workflows_running,
    coalesce(dw.completed, 0) AS workflows_completed,
    coalesce(c.total, 0) AS candidates,
    coalesce(c.pending, 0) AS pending,
    coalesce(c.promoted, 0) AS promoted,
    coalesce(c.duplicate, 0) AS duplicate,
    coalesce(c.rejected, 0) AS rejected,
    coalesce(c.failed, 0) AS failed,
    coalesce(s.active, 0) AS active_shares,
    e.first_seen_at
FROM events AS e
JOIN fixtures AS f ON f.id = e.fixture_id
LEFT JOIN LATERAL (
    SELECT
        count(*) FILTER (WHERE completed_at IS NULL) AS running,
        count(*) FILTER (WHERE completed_at IS NOT NULL) AS completed
    FROM event_downstream_workflows
    WHERE event_id = e.id
) AS dw ON true
LEFT JOIN LATERAL (
    SELECT
        count(*) AS total,
        count(*) FILTER (WHERE outcome_class = 'pending') AS pending,
        count(*) FILTER (WHERE outcome_class = 'promoted') AS promoted,
        count(*) FILTER (WHERE outcome_class = 'duplicate') AS duplicate,
        count(*) FILTER (WHERE outcome_class = 'rejected') AS rejected,
        count(*) FILTER (WHERE outcome_class = 'failed') AS failed
    FROM event_search_candidates
    WHERE event_id = e.id
) AS c ON true
LEFT JOIN LATERAL (
    SELECT count(*) FILTER (WHERE state = 'active') AS active
    FROM video_shares
    WHERE event_id = e.id
) AS s ON true
WHERE f.kickoff BETWEEN now() - interval '2 hours'
                    AND now() + make_interval(hours => :lookahead_hours)
   OR f.state = 'active'
ORDER BY f.kickoff, e.minute, e.extra NULLS FIRST, e.first_seen_at;

\echo ''
\echo 'Download failure taxonomy for those fixtures'
SELECT
    coalesce(c.outcome_detail#>>'{failure,stage}', 'legacy_unclassified') AS failure_stage,
    coalesce(c.outcome_detail#>>'{failure,class}', 'legacy_unclassified') AS failure_class,
    count(*) AS candidates,
    count(DISTINCT c.event_id) AS events,
    min(c.outcome_at) AS first_outcome,
    max(c.outcome_at) AS last_outcome
FROM event_search_candidates AS c
JOIN events AS e ON e.id = c.event_id
JOIN fixtures AS f ON f.id = e.fixture_id
WHERE c.outcome_class = 'failed'
  AND c.reject_reason = 'download_error'
  AND (
      f.kickoff BETWEEN now() - interval '2 hours'
                        AND now() + make_interval(hours => :lookahead_hours)
      OR f.state = 'active'
  )
GROUP BY failure_stage, failure_class
ORDER BY candidates DESC, failure_stage, failure_class;

\echo ''
\echo 'Candidate durability violations (completed parent with pending candidate)'
SELECT count(*) AS total_violations
FROM event_search_candidates AS c
WHERE c.outcome_class = 'pending'
  AND EXISTS (
      SELECT 1
      FROM event_downstream_workflows AS d
      WHERE d.event_id = c.event_id
        AND d.workflow_type = 'discovery'
        AND d.completed_at IS NOT NULL
  );

\echo 'Most recent violations (up to 10)'
SELECT
    e.fixture_id,
    c.event_id,
    c.search_attempt,
    c.tweet_url,
    c.discovered_at,
    d.completed_at AS parent_completed_at,
    d.outcome_class AS parent_outcome
FROM event_search_candidates AS c
JOIN events AS e ON e.id = c.event_id
JOIN LATERAL (
    SELECT completed_at, outcome_class
    FROM event_downstream_workflows
    WHERE event_id = c.event_id
      AND workflow_type = 'discovery'
      AND completed_at IS NOT NULL
    ORDER BY completed_at DESC
    LIMIT 1
) AS d ON true
WHERE c.outcome_class = 'pending'
ORDER BY d.completed_at DESC, c.discovered_at
LIMIT 10;

\set QUIET 1
COMMIT;
