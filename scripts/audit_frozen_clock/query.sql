-- Read-only raw clock evidence and current public-share inputs for an offline policy screen.
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '20s';
SELECT jsonb_build_object('source','snapshot','at',now(),'since','2026-08-31T00:00:00Z');
SELECT jsonb_build_object('source','accepted','id',v.id,'event_id',v.event_id,
  'asset_id',v.asset_id,'fixture_id',v.fixture_id,'player',e.player_name,
  'event_removed',e.removed,'at',v.recorded_at,
  'expected',v.evidence->'expected','frames',v.evidence->'frames',
  'evaluation',v.evidence->'evaluation','origin',v.evidence->'origin',
  'evaluator',v.evidence->>'evaluator','tolerance',v.evidence->'tolerance_minutes')
FROM video_asset_validations v JOIN events e ON e.id=v.event_id
ORDER BY v.recorded_at,v.id;
SELECT jsonb_build_object('source','rejected','id',c.id,'event_id',c.event_id,
  'fixture_id',c.fixture_id,'player',e.player_name,'event_removed',e.removed,
  'at',c.outcome_at,'current_elapsed',e.minute,'current_extra',e.extra,
  'reason',c.reject_reason,'detail',jsonb_build_object(
    'frame_observations',c.outcome_detail->'frame_observations',
    'expected_minute',c.outcome_detail->'expected_minute',
    'expected_period',c.outcome_detail->'expected_period'))
FROM event_search_candidates c JOIN events e ON e.id=c.event_id
WHERE c.outcome_class='rejected'
  AND jsonb_typeof(c.outcome_detail->'frame_observations')='array'
  AND c.discovered_at >= '2026-08-31T00:00:00Z'
ORDER BY c.outcome_at,c.id;
SELECT jsonb_build_object('source','share','id',s.id,'event_id',s.event_id,
  'asset_id',s.asset_id,'verified',s.timestamp_verified,'popularity',a.popularity,
  'file_size',a.file_size_bytes,'at',s.created_at)
FROM video_shares s JOIN video_assets a ON a.id=s.asset_id
WHERE s.state='active' AND a.superseded_by IS NULL AND a.object_reclaimed_at IS NULL
ORDER BY s.event_id,s.id;
ROLLBACK;
