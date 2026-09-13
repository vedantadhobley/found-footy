# Convert the saved FF-092 retained-asset NDJSON into the quality-audit CSV contract.
# Run: jq -rs -f scripts/audit_video_quality/history_to_csv.jq < saved-assets.ndjson
# Missing own verification inherits root category for topology only, just as query.sql does.
def lineage_root($rows; $row; $seen):
  if $row == null or ($seen | index($row.id)) != null then null
  elif $row.superseded_by == null then $row
  else $rows[$row.superseded_by] as $next
    | if $next.event_id != $row.event_id or $next.fixture_id != $row.fixture_id then null
      else lineage_root($rows; $next; $seen + [$row.id]) end
  end;

if length == 0 then error("empty retained-asset history") else . end
| . as $assets
| (map({key: .id, value: .}) | from_entries) as $rows
| if ($rows | length) != ($assets | length) then error("duplicate asset IDs") else . end
| (["event_id", "asset_id", "first_seen_at", "md5_hex", "hash_version",
    "frame_hashes_hex", "width", "height", "duration_ms", "file_size_bytes",
    "bitrate", "frame_rate", "popularity", "observed_popularity", "superseded_by",
    "timestamp_verified", "share_state", "share_id", "fixture_id", "player_name",
    "minute", "extra", "home_team_name", "away_team_name", "source_tweet_url",
    "event_removed", "object_reclaimed_at"],
  ($assets | sort_by(.event_id, .first_seen_at, .id)[]
    | . as $a | lineage_root($rows; $a; []) as $root
    | [ .event_id, .id, .first_seen_at, .md5, .hash_version, .hashes,
        .width, .height, .duration_ms, .file_size_bytes, (.bitrate // 0), (.frame_rate // 0),
        .popularity, (.observations // 0), (.superseded_by // ""),
        (if .verified != null then .verified else ($root.verified // false) end),
        (.share_state // "observed"), (.share_id // ""), .fixture_id, (.player_name // ""),
        .minute, (.extra // ""), .home_team_name, .away_team_name, "",
        .event_removed, (.object_reclaimed_at // "") ]))
| @csv
