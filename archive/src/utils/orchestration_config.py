"""
Centralized tuning parameters for the orchestration pipeline.

Every workflow / activity / store-method constant that used to live inline
in code now lives here, with the rationale documented next to it. Edit
here to retune; grep for the constant name to find call sites.

Sprint 4B (2026-05-26). Pre-Phase-1 work — this file is the "knobs"
surface that subsequent observability + error-handling phases will refer
back to ("what's our current SLO for X? — see orchestration_config").

Conventions:
  - SECONDS / MINUTES / etc. suffixes specify the unit (no `datetime.timedelta`
    objects in this module — callers wrap as needed).
  - Group by subsystem (TWITTER_*, MONITOR_*, UPLOAD_*, LLM_*).
  - One source of truth per logical knob; if a value appears in two
    subsystems (e.g. the "10 downloads required" threshold is both a
    Twitter loop exit condition and a download-completion mark trigger),
    they share a single constant.
"""

# ─── Monitor / event debounce ────────────────────────────────────────────────
#
# A new event (e.g. a goal just detected by API-Football) needs to be seen
# by N consecutive monitor polls before we trust it and trigger Twitter
# search. Same threshold in reverse: an event needs to DISAPPEAR from the
# API for N consecutive polls before we delete it (VAR debounce).
#
# 3 polls × 30s monitor cadence = 90s confirmation window in both
# directions. Tightening would risk acting on API glitches; loosening
# would delay video search for early goals.

MONITOR_DEBOUNCE_STABLE_COUNT = 3
MONITOR_DROP_THRESHOLD = 3

# How many minutes ahead of kickoff do we activate a staging fixture (move
# it from fixtures_staging → fixtures_active so MonitorWorkflow starts
# polling it every 30s)? 30 min gives us a buffer for early-start TV
# friendlies. Below 10 min would risk missing the actual kickoff.

MONITOR_STAGING_LOOKAHEAD_MINUTES = 30

# Staging-fixture polling cadence. Active fixtures poll every 30s (the
# MonitorWorkflow schedule); staging fixtures poll once per 15-minute
# wall-clock interval since they rarely change. This reduces API spend
# substantially.

MONITOR_STAGING_INTERVAL_MINUTES = 15


# ─── Twitter discovery loop ──────────────────────────────────────────────────
#
# When a goal-event passes debounce, we spawn a TwitterWorkflow that loops
# up to MAX_ATTEMPTS times, kicking off a DownloadWorkflow per attempt.
# Each attempt is ATTEMPT_SPACING_SECONDS apart (measured START-to-START
# so a slow search doesn't push later attempts late).
#
# We exit the loop early when REQUIRED_DOWNLOADS DownloadWorkflows have
# registered themselves — that signals the event has "enough" video
# coverage. The safety cap MAX_ATTEMPTS > REQUIRED_DOWNLOADS handles
# transient DLWF spawn failures.
#
# Each search attempt picks up to MAX_VIDEOS_PER_ATTEMPT longest videos
# (sorted by duration desc) and fires one DownloadWorkflow with that list.

TWITTER_MAX_ATTEMPTS = 15
TWITTER_REQUIRED_DOWNLOADS = 10
TWITTER_MAX_VIDEOS_PER_ATTEMPT = 5

# Twitter search filters to tweets posted in the last N minutes. Goal
# clips appear on Twitter ~30s–5min after the goal; older tweets are
# usually re-shares or unrelated.

TWITTER_SEARCH_MAX_AGE_MINUTES = 3

# Spacing between Twitter search attempts. The min-wait floor prevents
# the loop from spinning when an attempt completes very fast (e.g.
# Twitter returns an immediate cached empty result).

TWITTER_ATTEMPT_SPACING_SECONDS = 60
TWITTER_ATTEMPT_MIN_WAIT_SECONDS = 10


# ─── Upload workflow ─────────────────────────────────────────────────────────
#
# UploadWorkflow is signal-with-start per event_id. Once started, it
# processes incoming DLWF batches FIFO and exits after IDLE_TIMEOUT of no
# new signals. The post-Sprint-1 design has DLWFs run the
# check_and_mark_download_complete activity at their own exit, so this
# idle timeout is now a *failsafe* — covers the rare case where every
# DLWF dies before reaching its finally block.

UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES = 5


# ─── Phase 4 — Discovery hardening ───────────────────────────────────────────
#
# Per-match coverage SLO: alert when a tracked-league fixture finishes
# with fewer than MATCH_COVERAGE_SLO_THRESHOLD of its goals captured to
# S3. The threshold is permissive (0.5 = half) so it only fires on real
# pipeline regressions, not on the random goal we miss because X didn't
# index a clip in time.
#
# Tracked-league IDs are the top-5 European leagues by default; extend
# with UEFA continental + FIFA international IDs when those competitions
# are in play (World Cup leagues land here ~3 weeks out from kickoff).

MATCH_COVERAGE_SLO_THRESHOLD = 0.5

SLO_TRACKED_LEAGUE_IDS = {
    39,    # Premier League
    140,   # La Liga
    78,    # Bundesliga
    135,   # Serie A
    61,    # Ligue 1
    2,     # UEFA Champions League
    3,     # UEFA Europa League
    848,   # UEFA Conference League
    # FIFA World Cup (1) + UEFA Euro (4) etc. — add when those competitions are active.
}

# DOM-selector canary — small synthetic search that runs hourly and
# verifies X's tweet markup still matches our scraper's selectors. Lets
# us notice an X redesign within an hour instead of "where did the
# goals go?" hours later.
#
# Wide search window (24h) so the canary tests STRUCTURAL parsing not
# live-freshness — outside live match hours the scraper's age-filter
# would otherwise bail before we can validate the markup. The narrow
# window for actual goal searches lives in TWITTER_SEARCH_MAX_AGE_MINUTES.

DOM_CANARY_QUERY = "football goal"
DOM_CANARY_MAX_AGE_MINUTES = 1440  # 24h — widen past the age-filter early-exit
DOM_CANARY_MIN_TWEETS = 3  # at least this many results to consider the canary green


# ─── LLM concurrency ─────────────────────────────────────────────────────────
#
# joi's llama-small (Qwen3.5-9B VL) is configured with --parallel 2 — it
# can decode 2 streams concurrently. This semaphore limits each worker
# process to that same 2-concurrent budget on the assumption joi runs ~1
# worker. With 8 worker replicas + this Semaphore(2) per process, the
# effective in-flight cap against joi is 16, not 2 (audit'd as a real
# scaling concern). Phase 1 of the rewrite roadmap addresses this with
# a workspace-level LLM gateway; until then, this constant documents the
# per-worker-process intent.

LLM_CONCURRENCY_PER_WORKER = 2
