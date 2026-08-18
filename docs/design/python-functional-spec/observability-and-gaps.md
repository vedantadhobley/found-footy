# Python observability, gaps, and summary

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 15. Observability & Telemetry

### Logging Strategy

All modules use centralized `log` module
(`archive/src/utils/footy_logging.py`) with structured fields:

```
log.info(logger, MODULE, action, message, **fields)
log.warning(logger, MODULE, action, message, **fields)
log.error(logger, MODULE, action, message, **fields)
```

**MODULE:** String identifying the workflow/activity (e.g.,
"monitor", "twitter", "ingest")

**action:** String identifying the event (e.g., "new_event",
"video_rejected", "completion_started")

All logs go to **Loki** (via structured logging) and **Grafana** for
alerting/dashboarding.

### Named Log Actions (Sampled)

#### Ingest Module

| Action | When | Key Fields |
| --- | --- | --- |
| `fetch_fixtures_started` | Ingest begins | target_date |
| `fixtures_retrieved` | API call returns | total |
| `fixtures_filtered` | Team filter applied | filtered, removed |
| `categorized` | Fixtures routed | total, staging, active, completed |
| `stored` | MongoDB inserts | staging, active, completed |
| `skipped_existing` | Duplicates detected | count |

#### Monitor Module

| Action | When | Key Fields |
| --- | --- | --- |
| `staging_poll` | Staging check starts | polling, total |
| `pre_activated` | Fixture moved to active | fixture_id, kickoff_in_minutes |
| `emergency_activation` | Game started in staging | fixture_id, status |
| `new_event` | Event detected | event_id, player_status |
| `monitoring` | Event tracking | event_id, count, max_count |
| `ready_for_twitter` | Event debounced | event_id, monitor_workflows |
| `var_removed` | Event deleted | event_id, drop_workflows |
| `match_completed_summary` | Fixture completes | goals_total, coverage_rate, failure_classes_total |
| `match_below_slo` | Coverage SLO triggered | league_name, coverage_rate, goals_total |

#### Twitter Module

| Action | When | Key Fields |
| --- | --- | --- |
| `started` | Workflow begins | event_id, team_id, player_names |
| `alias_cache_hit` | RAG cache hit | aliases |
| `rag_success` | RAG pipeline completed | aliases |
| `search_query` | Query built | query, excluding_count |
| `search_complete` | Twitter search returns | found, query |
| `attempt_search_complete` | Attempt finished | attempt, unique_videos |
| `download_count_reached` | Exit loop | download_count, event_id |
| `event_deleted` | VAR detected | event_id |
| `loop_complete` | TwitterWorkflow exits | reason, download_count, attempts |

#### Download Module

| Action | When | Key Fields |
| --- | --- | --- |
| `started` | Workflow begins | event_id, videos |
| `registered` | Self-registration | workflow_id, download_count, event_id |
| `downloads_complete` | Download stage done | success, filtered, failed |
| `batch_dedup_complete` | MD5 dedup | unique, batch_dupes |
| `validation_complete` | AI validation done | passed, rejected, validation_errors |
| `hash_generation_complete` | Perceptual hashing | generated, total |
| `workflow_complete` | Workflow exits | uploaded, s3_urls, **download_stats |

#### Upload Module

| Action | When | Key Fields |
| --- | --- | --- |
| `batch_received` | Signal delivered | videos, queue_size |
| `md5_dedup_complete` | MD5 dedup | unique, batch_dupes, s3_matches, s3_replacements |
| `perceptual_dedup_complete` | Perceptual dedup | new, replacements, skipped, verified_new, verified_replaced |
| `uploads_complete` | S3 uploads done | success, total |
| `saved_to_mongodb` | MongoDB update | count |
| `workflow_complete` | UploadWorkflow exits | total_uploaded, batches |

### Telemetry Emission Points

**Per-match summary** (logged at fixture completion):
- `match_completed_summary` log line (Loki JSON queryable)
- Fields: goals_total, videos_captured_total, coverage_rate,
  failure_classes_total, time_to_first_s3_p50_s

**Per-event telemetry** (stored in `_telemetry` field):
- search_attempts, videos_discovered, videos_validated,
  download_failures_by_class
- first_seen_at, first_s3_upload_at (for latency metrics)

**Per-download-workflow stats** (stored in `_download_stats` field):
- discovered, downloaded, md5_batch_deduped, ai_rejected,
  hash_generated, uploaded
- Breakdown by failure class

---

## 16. Gaps in This Spec

The following behaviors are **unclear from code** or
**undocumented**, and would need testing/clarification before Go
rewrite implementation:

1. **API-Football response format for fixtures on same day,
   different times** — does `get_fixtures_for_date()` return ALL
   fixtures for that UTC date? How are UTC-±N timezones handled?

2. **Duplicate event_id collision** — should there be a unique
   index on `(fixture_id, event_id)` within the events array?

3. **VAR reversal edge case: what if event reappears after 3-drop
   deletion?** Would sequence number reset or continue from before?

4. **Perceptual hash collision with different goals** — could two
   different goals have similar enough hashes to trigger false
   dedup? Hamming distance threshold is UNCLEAR (not in provided code).

5. **Timezone handling in ingest lookahead** — is 3-day lookahead
   sufficient to catch all timezones from UTC-12 to UTC+14?

6. **Twitter service discovery in production (TWITTER_SCALED)** —
   what if all 8 instances are unhealthy? Round-robin? Failover?

7. **Wikidata SPARQL rate limiting** — no documented rate limit
   handling. Retry or fail?

8. **MongoDB write throughput** — concurrent monitors + uploads +
   ingests all writing to fixtures_active. No formal concurrency
   control. Could lost updates happen?

9. **S3 object metadata (file size, duration, bitrate)** — where
   stored? S3 object tags? MongoDB metadata? Extracted at upload
   time or passed in?

10. **Temporal workflow replay semantics** — could old
    MonitorWorkflow IDs re-appear in replayed workflows?

11. **Event player.id = 0 handling (own goals)** — do own goals
    ever get triggered for Twitter, or do they get stuck at
    "unknown player"?

12. **Fixture status cancellation mid-ingest** — is CANC-during-
    ingest handled correctly?

13. **Download workflow failure classes** — full list of error
    classes not provided. Fallback for unknown errors?

14. **Alias cache invalidation** — no TTL on team_aliases. If team
    changes name, when is cache refreshed?

15. **Monitor cycle timing under load** — if processing takes 35s,
    does next cycle start immediately or wait?

---

## Summary

The Python found-footy system is a **real-time event-driven
pipeline** with clear data flow (Ingest → Monitor → Twitter →
Download → Upload) and robust failure handling through **Temporal
workflows**. The specification above captures **what the system
actually does** at the level of detail needed for a faithful Go
rewrite.

Key invariants:
- **One TwitterWorkflow per event** (stable ID, server-enforced dedup)
- **Workflow-ID-based tracking** (idempotent registration via arrays)
- **Serialized per-event uploads** (FIFO signal queue via Temporal)
- **Scoped deduplication** (verified videos only compared against
  verified S3)
- **3-poll debounce** (both event confirmation and VAR reversal)
- **14-day retention** (fixtures auto-expire, self-cleaning)
- **Fire-and-forget child workflows** (ABANDON policy, independent
  failure domains)

The system tolerates transient failures well (API glitches, network
timeouts, LLM unavailability) but has known edge cases around
concurrent modifications, orphaned S3 blobs, and unclear timezone
semantics. The `dedup.py:415-420` first-match-only bug is flagged as
`BUG?` for the Go rewrite to decide whether to preserve or fix.
