#!/usr/bin/env bash
# Operational health snapshot — primarily a canary for the Lazio Pisa
# failure pattern (events stuck with _download_workflows >= 10 AND
# _download_complete = false). After Sprint 1, this pattern should be
# impossible. This script is how we confirm that empirically.
#
# Also reports:
#   - Active fixtures + per-fixture event progress
#   - Staging fixtures (upcoming kickoffs)
#   - Recent failed workflows in Temporal (last 1 hour)
#
# Usage:
#   scripts/check_stuck_events.sh              # prod by default
#   FOOTY_ENV=dev scripts/check_stuck_events.sh

set -euo pipefail
ENV="${FOOTY_ENV:-prod}"
MONGO="found-footy-${ENV}-mongo"
TEMPORAL="found-footy-${ENV}-temporal"

NOW_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ONE_HOUR_AGO="$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)"

echo "=== Stuck-event check (env=${ENV}, time=${NOW_ISO}) ==="
docker exec "$MONGO" mongosh -u ffuser -p ffpass \
  --authenticationDatabase admin found_footy --quiet --eval '
const stuck = [];
function scan(coll, name) {
  db[coll].find({}, {_id:1, "teams.home.name":1, "teams.away.name":1, events:1}).forEach(f => {
    (f.events || []).forEach(e => {
      const dwLen = (e._download_workflows || []).length;
      if (dwLen >= 10 && e._download_complete === false) {
        stuck.push({
          coll: name,
          fixture: f._id,
          match: (f.teams && f.teams.home ? f.teams.home.name : "?") + " vs " +
                 (f.teams && f.teams.away ? f.teams.away.name : "?"),
          event: e._event_id,
          minute: e.time ? e.time.elapsed + (e.time.extra ? "+"+e.time.extra : "") : "?",
          player: e.player ? e.player.name : "?",
          s3_videos: (e._s3_videos || []).length,
          dlwfs: dwLen
        });
      }
    });
  });
}
scan("fixtures_active", "active");
scan("fixtures_completed", "completed");
if (stuck.length === 0) {
  print("  OK — no stuck events");
} else {
  print("  STUCK (" + stuck.length + "):");
  stuck.forEach(s => print("    [" + s.coll + "] " + s.fixture + " " + s.match +
                          " — " + s.minute + "min " + s.player +
                          " (dlwfs=" + s.dlwfs + ", s3_videos=" + s.s3_videos +
                          ", event=" + s.event + ")"));
}

print("");
print("=== Active fixtures ===");
const active = db.fixtures_active.find({}, {_id:1, "teams.home.name":1, "teams.away.name":1, "fixture.status":1, events:1}).toArray();
if (active.length === 0) {
  print("  (none active)");
} else {
  active.forEach(f => {
    const events = f.events || [];
    const inProgress = events.filter(e => e._monitor_complete && !e._download_complete).length;
    const complete = events.filter(e => e._download_complete).length;
    const status = f.fixture && f.fixture.status ? (f.fixture.status.short + " " + (f.fixture.status.elapsed || 0) + "min") : "?";
    print("  " + f._id + " " + f.teams.home.name + " vs " + f.teams.away.name +
          " — " + status + " — events: " + events.length + " total, " +
          inProgress + " in-progress, " + complete + " complete");
  });
}

print("");
print("=== Staging (upcoming) ===");
db.fixtures_staging.find({}, {_id:1, "teams.home.name":1, "teams.away.name":1, "fixture.date":1}).sort({"fixture.date":1}).forEach(f => {
  print("  " + f._id + " " + f.fixture.date.substr(0,16) + " " +
        f.teams.home.name + " vs " + f.teams.away.name);
});
'

echo ""
echo "=== Recent Failed/Terminated workflows (since ${ONE_HOUR_AGO}) ==="
docker exec "$TEMPORAL" temporal workflow list \
  --query 'ExecutionStatus IN ("Failed", "Terminated", "TimedOut") AND CloseTime > "'"$ONE_HOUR_AGO"'"' \
  --address "${TEMPORAL}:7233" --limit 20 2>&1 | head -25

echo ""
echo "=== Worker health ==="
docker ps --filter "name=found-footy-${ENV}-worker" --format "  {{.Names}}: {{.Status}}"
