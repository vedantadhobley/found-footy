-- One-event FF-081 restoration with four fresh own evaluations; transaction required.
-- The operation token prevents accidents; it is not production authorization.
DO $repair$
DECLARE
    target CONSTANT uuid := '01dedc54-da16-4e3c-9539-ac43c81ffd77';
    keeper CONSTANT uuid := 'f5274482-6ef0-5a5f-b84f-d07b7eefa79c';
    restored CONSTANT uuid := '32b50be4-0ad5-50a5-8ebe-f590cc4b57a4';
    input jsonb;
    snapshot jsonb;
    plan jsonb;
    current_assets jsonb;
    current_shares jsonb;
    current_sources jsonb;
    expected_assets jsonb;
    expected_shares jsonb;
    expected_sources jsonb;
    actual_evidence jsonb;
    untouched_before jsonb;
    untouched_after jsonb;
    prior_hash text;
    prior_result jsonb;
    repaired boolean;
    n integer;
    pass integer;
BEGIN
    IF current_setting('found_footy.repair_approval',true) IS DISTINCT FROM 'ff081-maitland-niles' THEN
        RAISE EXCEPTION 'FF081 repair disabled';
    END IF;
    PERFORM set_config('lock_timeout','5s',true);
    IF (SELECT count(*) FROM pg_temp.repair_input) <> 1 THEN RAISE EXCEPTION 'one repair input required'; END IF;
    SELECT data INTO input FROM pg_temp.repair_input;
    snapshot:=input->'snapshot'; plan:=input->'result'->'Plan';
    IF snapshot#>>'{event,id}' IS DISTINCT FROM target::text
       OR snapshot#>>'{event,fixture_id}' IS DISTINCT FROM '1557390'
       OR input->>'snapshot_hash' IS DISTINCT FROM '4dd4ef33ac4a34a6b13e5ba898e4a3d44ad72e16d0af1ef9c2f0cd6f6e06528b'
       OR plan->>'Version' IS DISTINCT FROM 'direct-restoration-support-v2'
       OR plan->'Restored' IS DISTINCT FROM jsonb_build_array(restored)
       OR jsonb_array_length(plan->'Selected') <> 4
       OR jsonb_array_length(plan->'Owners') <> 10
       OR jsonb_array_length(input->'evidence') <> 4 THEN
        RAISE EXCEPTION 'unexpected repair identity or plan';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_to_recordset(plan->'Selected') x("AssetID" uuid,"Popularity" int,"Verified" bool,"ExtractedMinute" int)
        WHERE x."AssetID" NOT IN (keeper,restored,'8c19fa8b-72f0-566e-b1e6-f6f67ee9407d','191fc025-9b61-5c87-9398-dedd824477c1')
           OR x."Popularity" <> CASE x."AssetID" WHEN keeper THEN 31 WHEN restored THEN 6 ELSE 1 END
           OR x."Verified" IS DISTINCT FROM false OR x."ExtractedMinute" IS NOT NULL)
       OR (SELECT count(DISTINCT v->>'AssetID') FROM jsonb_array_elements(plan->'Selected') v)<>4
       OR (SELECT count(DISTINCT v->>'AssetID') FROM jsonb_array_elements(plan->'Owners') v)<>10 THEN
        RAISE EXCEPTION 'unexpected keeper scores or validation';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_to_recordset(plan->'Owners') x("AssetID" uuid,"KeeperID" uuid)
        WHERE x."KeeperID" IS DISTINCT FROM CASE
          WHEN x."AssetID" IN (restored,'8c19fa8b-72f0-566e-b1e6-f6f67ee9407d','191fc025-9b61-5c87-9398-dedd824477c1') THEN x."AssetID" ELSE keeper END)
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(input->'evidence') x
          WHERE x->>'asset_id' NOT IN ('d0ef0afe-7c92-50e1-875d-15a35c78df7d','3227d1d5-c2d8-56fa-8dca-7238929b6040','b609a521-9873-57f6-af56-bd3fa3326bbc','358b5db8-a107-5f48-ab28-1f440d05718e')
             OR x#>>'{evidence,event_id}' IS DISTINCT FROM target::text
             OR x#>>'{evidence,fixture_id}' IS DISTINCT FROM '1557390'
             OR x#>>'{evidence,evaluation,Outcome}' IS DISTINCT FROM 'unverified')
       OR (SELECT count(DISTINCT v->>'asset_id') FROM jsonb_array_elements(input->'evidence') v)<>4 THEN
        RAISE EXCEPTION 'unexpected owners or fresh evaluation scope';
    END IF;

    -- Event-first ordering is shared with placement and removal; no fixture writes.
    PERFORM 1 FROM events WHERE id=target AND fixture_id=1557390 FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'event missing'; END IF;
    PERFORM 1 FROM event_downstream_workflows WHERE event_id=target ORDER BY workflow_id FOR UPDATE;
    PERFORM 1 FROM video_assets WHERE event_id=target ORDER BY id FOR UPDATE;
    PERFORM 1 FROM video_shares WHERE event_id=target ORDER BY id FOR UPDATE;
    PERFORM 1 FROM event_search_candidates WHERE event_id=target ORDER BY id FOR UPDATE;
    IF (SELECT to_jsonb(e) FROM events e WHERE id=target) IS DISTINCT FROM snapshot->'event'
       OR NOT EXISTS (SELECT 1 FROM events e JOIN fixtures f ON f.id=e.fixture_id
           WHERE e.id=target AND NOT e.removed AND e.minute=90 AND e.extra=6 AND f.state='completed')
       OR (SELECT count(*) FROM event_downstream_workflows WHERE event_id=target)<>1
       OR NOT EXISTS (SELECT 1 FROM event_downstream_workflows WHERE event_id=target
           AND workflow_id='event-'||target::text AND completed_at IS NOT NULL AND outcome_class='assets_surfaced')
       OR EXISTS (SELECT 1 FROM event_search_candidates WHERE event_id=target AND outcome_class='pending') THEN
        RAISE EXCEPTION 'event changed, removed, or downstream work pending';
    END IF;
    IF EXISTS (SELECT 1 FROM video_assets WHERE event_id=target AND
       (object_reclaimed_at IS NOT NULL OR s3_bucket<>'found-footy' OR s3_key<>'assets/1557390/'||target::text||'/'||id::text||'.mp4')) THEN
        RAISE EXCEPTION 'media metadata no longer eligible';
    END IF;
    SELECT request_hash,result INTO prior_hash,prior_result FROM video_selection_commits WHERE id=(input->>'id')::uuid;
    repaired:=FOUND;
    IF repaired AND (prior_hash IS DISTINCT FROM input->>'request_hash' OR prior_result IS DISTINCT FROM input->'result') THEN
        RAISE EXCEPTION 'repair receipt differs';
    END IF;
    IF (SELECT count(*) FROM video_selection_commits WHERE event_id=target)<>(CASE WHEN repaired THEN 1 ELSE 0 END) THEN
        RAISE EXCEPTION 'unexpected selection receipt';
    END IF;

    SELECT jsonb_agg(v || jsonb_build_object('superseded_by',CASE WHEN x."AssetID"=x."KeeperID" THEN NULL ELSE to_jsonb(x."KeeperID") END,
        'popularity',COALESCE(y."Popularity",(v->>'popularity')::int)) ORDER BY v->>'id') INTO expected_assets
      FROM jsonb_array_elements(snapshot->'assets') v
      JOIN jsonb_to_recordset(plan->'Owners') x("AssetID" uuid,"KeeperID" uuid) ON x."AssetID"=(v->>'id')::uuid
      LEFT JOIN jsonb_to_recordset(plan->'Selected') y("AssetID" uuid,"Popularity" int) ON y."AssetID"=x."AssetID";
    SELECT jsonb_agg(CASE WHEN v->>'id'='s_19a9f2fb7ea2' THEN v||'{"state":"active","rank":7}'::jsonb ELSE v END ORDER BY v->>'id')
      INTO expected_shares FROM jsonb_array_elements(snapshot->'shares') v;
    SELECT jsonb_agg(v||jsonb_build_object('credited_asset_id',x."KeeperID") ORDER BY v->>'id') INTO expected_sources
      FROM jsonb_array_elements(snapshot->'sources') v
      JOIN jsonb_to_recordset(plan->'Owners') x("AssetID" uuid,"KeeperID" uuid) ON x."AssetID"=(v->>'observed_asset_id')::uuid;
    IF jsonb_array_length(expected_assets)<>10 OR jsonb_array_length(expected_shares)<>6 OR jsonb_array_length(expected_sources)<>34 THEN
        RAISE EXCEPTION 'incomplete repair inventory';
    END IF;

    FOR pass IN 0..1 LOOP
        SELECT jsonb_agg(to_jsonb(a) ORDER BY id) INTO current_assets FROM video_assets a WHERE event_id=target;
        SELECT jsonb_agg(to_jsonb(s) ORDER BY id) INTO current_shares FROM video_shares s WHERE event_id=target;
        SELECT jsonb_agg(jsonb_build_object('id',id,'observed_asset_id',observed_asset_id,'credited_asset_id',credited_asset_id) ORDER BY id)
          INTO current_sources FROM event_search_candidates WHERE event_id=target AND outcome_class IN ('promoted','duplicate','superseded');
        IF pass=1 OR repaired THEN
            IF current_assets IS DISTINCT FROM expected_assets OR current_shares IS DISTINCT FROM expected_shares
               OR current_sources IS DISTINCT FROM expected_sources THEN RAISE EXCEPTION 'repair post-state differs'; END IF;
        ELSE
            IF current_assets IS DISTINCT FROM (SELECT jsonb_agg(v ORDER BY v->>'id') FROM jsonb_array_elements(snapshot->'assets') v)
               OR current_shares IS DISTINCT FROM (SELECT jsonb_agg(v ORDER BY v->>'id') FROM jsonb_array_elements(snapshot->'shares') v)
               OR current_sources IS DISTINCT FROM (SELECT jsonb_agg(v ORDER BY v->>'id') FROM jsonb_array_elements(snapshot->'sources') v) THEN
                RAISE EXCEPTION 'stale reviewed snapshot';
            END IF;
        END IF;
        SELECT COALESCE(jsonb_agg(jsonb_build_object('asset_id',asset_id,'evidence',evidence) ORDER BY asset_id),'[]') INTO actual_evidence
          FROM video_asset_validations WHERE event_id=target;
        IF actual_evidence IS DISTINCT FROM (CASE WHEN pass=1 OR repaired
          THEN (SELECT jsonb_agg(v ORDER BY v->>'asset_id') FROM jsonb_array_elements(input->'evidence') v) ELSE '[]'::jsonb END) THEN
            RAISE EXCEPTION 'own validation evidence changed';
        END IF;

        -- Every other fixture row and every immutable candidate field is preserved.
        SELECT jsonb_build_object(
          'fixture',(SELECT to_jsonb(f) FROM fixtures f WHERE id=1557390),
          'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE fixture_id=1557390),
          'downstream',(SELECT jsonb_agg(to_jsonb(d) ORDER BY event_id,workflow_id) FROM event_downstream_workflows d WHERE event_id IN (SELECT id FROM events WHERE fixture_id=1557390)),
          'other_assets',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM video_assets a WHERE fixture_id=1557390 AND event_id<>target),
          'other_shares',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM video_shares s WHERE event_id IN (SELECT id FROM events WHERE fixture_id=1557390 AND id<>target)),
          'candidates',(SELECT jsonb_agg(CASE WHEN c.event_id=target THEN to_jsonb(c)-'credited_asset_id' ELSE to_jsonb(c) END ORDER BY id) FROM event_search_candidates c WHERE fixture_id=1557390)
        ) INTO untouched_after;
        IF pass=1 THEN
            IF untouched_after IS DISTINCT FROM untouched_before THEN RAISE EXCEPTION 'unexpected immutable or other-event change'; END IF;
            EXIT;
        END IF;
        IF repaired THEN
            RAISE NOTICE 'FF081_REPAIR_ALREADY_APPLIED: no rows changed'; RETURN;
        END IF;
        untouched_before:=untouched_after;

        INSERT INTO video_asset_validations(id,asset_id,event_id,fixture_id,evidence)
          SELECT (v#>>'{evidence,id}')::uuid,(v->>'asset_id')::uuid,target,1557390,v->'evidence'
          FROM jsonb_array_elements(input->'evidence') v;
        GET DIAGNOSTICS n=ROW_COUNT;
        IF n<>4 THEN RAISE EXCEPTION 'expected four fresh evaluations'; END IF;
        IF EXISTS (SELECT 1 FROM video_asset_validations v JOIN video_assets a ON a.id=v.asset_id
           WHERE v.event_id=target AND (v.evidence->>'md5' IS DISTINCT FROM encode(a.md5,'hex')
             OR EXISTS(SELECT 1 FROM video_shares s WHERE s.asset_id=a.id))) THEN
            RAISE EXCEPTION 'evaluation does not describe a never-public original';
        END IF;
        UPDATE video_assets a SET superseded_by=x.superseded_by,popularity=x.popularity
          FROM jsonb_to_recordset(expected_assets) x(id uuid,superseded_by uuid,popularity int)
          WHERE a.event_id=target AND a.id=x.id
            AND (a.superseded_by IS DISTINCT FROM x.superseded_by OR a.popularity<>x.popularity);
        UPDATE event_search_candidates c SET credited_asset_id=x.credited_asset_id
          FROM jsonb_to_recordset(expected_sources) x(id uuid,credited_asset_id uuid)
          WHERE c.event_id=target AND c.id=x.id AND c.credited_asset_id IS DISTINCT FROM x.credited_asset_id;
        GET DIAGNOSTICS n=ROW_COUNT;
        IF n<>1 THEN RAISE EXCEPTION 'expected one canonical source reassignment'; END IF;
        UPDATE video_shares SET state='active',rank=7
          WHERE event_id=target AND id='s_19a9f2fb7ea2' AND asset_id=restored AND state='superseded';
        GET DIAGNOSTICS n=ROW_COUNT;
        IF n<>1 THEN RAISE EXCEPTION 'expected one share restoration'; END IF;
    END LOOP;
    INSERT INTO video_selection_commits(id,event_id,fixture_id,request_hash,snapshot_hash,policy,before_state,result)
      VALUES((input->>'id')::uuid,target,1557390,input->>'request_hash',input->>'snapshot_hash',input->'policy',input->'before_state',input->'result');
    RAISE NOTICE 'FF081_REPAIR_VALIDATED: event %, receipt %, public 31/6; caller must COMMIT',target,input->>'id';
END
$repair$;
