#!/usr/bin/env bash
# Exercise only the explicitly named disposable database; never a production target.
set -euo pipefail
database=found-footy-maitland-repair-test-20260913
base=scratch-audit-2026-09-13/maitland-niles-review
bundle="$base/repair-run-v2.sql"

# digest includes all seeded rows plus both new evidence tables, excluding no data.
digest() {
    docker exec "$database" psql -X -A -t -U postgres -v ON_ERROR_STOP=1 -c "SELECT md5(jsonb_build_object(
      'fixtures',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM fixtures t),
      'events',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM events t),
      'assets',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM video_assets t),
      'shares',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM video_shares t),
      'candidates',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM event_search_candidates t),
      'downstream',(SELECT jsonb_agg(to_jsonb(t) ORDER BY event_id,workflow_id) FROM event_downstream_workflows t),
      'evaluations',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM video_asset_validations t),
      'receipts',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM video_selection_commits t))::text);"
}

# failure injects a conflicting state inside the repair transaction and requires full rollback.
failure() {
    local label="$1" injection="$2" before after result
    before=$(digest)
    set +e
    awk -v injection="$injection" '/^DO \$repair\$/ {print injection} {print}' "$bundle" |
        docker exec -i "$database" psql -X -U postgres -v ON_ERROR_STOP=1 > "$base/repair-test-$label.log" 2>&1
    result=$?
    set -e
    after=$(digest)
    if [[ "$result" -eq 0 || "$before" != "$after" ]]; then
        printf 'FAIL %s exit=%s before=%s after=%s\n' "$label" "$result" "$before" "$after"
        exit 1
    fi
    printf 'PASS %s: rejected; complete database unchanged\n' "$label"
    tail -n 2 "$base/repair-test-$label.log"
}

failure disabled "SET LOCAL found_footy.repair_approval='disabled';"
failure stale_score "UPDATE video_assets SET popularity=popularity+1 WHERE id='f5274482-6ef0-5a5f-b84f-d07b7eefa79c';"
failure removed "UPDATE events SET removed=true,removed_reason='var',removed_at=now() WHERE id='01dedc54-da16-4e3c-9539-ac43c81ffd77';"
failure pending "UPDATE event_downstream_workflows SET completed_at=NULL,outcome_class=NULL WHERE event_id='01dedc54-da16-4e3c-9539-ac43c81ffd77';"
failure missing_media "UPDATE video_assets SET object_reclaimed_at=now() WHERE id='32b50be4-0ad5-50a5-8ebe-f590cc4b57a4';"
failure wrong_md5 "UPDATE repair_input SET data=jsonb_set(data,'{evidence,0,evidence,md5}',to_jsonb(repeat('0',32)));"
failure rank_collision "UPDATE video_shares SET rank=7 WHERE id='s_4973f6c646ae';"

docker exec -i "$database" psql -X -U postgres -v ON_ERROR_STOP=1 < "$bundle" > "$base/repair-test-commit.log" 2>&1
tail -n 3 "$base/repair-test-commit.log"
before=$(digest)
docker exec -i "$database" psql -X -U postgres -v ON_ERROR_STOP=1 < "$bundle" > "$base/repair-test-retry.log" 2>&1
after=$(digest)
[[ "$before" == "$after" ]]
rg -q 'FF081_REPAIR_ALREADY_APPLIED' "$base/repair-test-retry.log"
printf 'PASS retry: complete database unchanged\n'
failure changed_post_state "UPDATE video_assets SET popularity=popularity+1 WHERE id='32b50be4-0ad5-50a5-8ebe-f590cc4b57a4';"
printf 'REPAIR_REHEARSAL_SUCCESS\n'
