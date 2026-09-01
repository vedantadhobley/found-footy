-- schema-hash: d680ec63b34a46db1f42a3fe549926c4adee8b6d38daa2c8d73f29a18715efae
-- Repairs pending candidates owned by removed events without touching unrelated historical residue.

UPDATE event_search_candidates AS candidate
SET outcome_class = 'rejected',
    reject_reason = 'event_removed',
    outcome_detail = CASE
        WHEN candidate.outcome_detail IS NULL THEN '{}'::jsonb
        WHEN jsonb_typeof(candidate.outcome_detail) = 'object' THEN candidate.outcome_detail
        ELSE jsonb_build_object('previous_detail', candidate.outcome_detail)
    END || jsonb_build_object('reason', 'event_removed'),
    outcome_at = NOW(),
    credited_asset_id = NULL
FROM events AS event
WHERE event.id = candidate.event_id
  AND event.removed
  AND candidate.outcome_class = 'pending';
