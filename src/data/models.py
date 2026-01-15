"""
Found Footy Data Models
=======================

TypedDict models defining the data structures used throughout the application.

WHY TYPEDDICT (not Pydantic)?
─────────────────────────────
1. MongoDB documents ARE dicts - TypedDict is semantically correct
2. Allows underscore-prefixed field names (our enhancement fields)
3. No runtime overhead - pure type hints for IDE/mypy
4. Works naturally with pymongo which returns plain dicts

These models serve as:
1. Living documentation of our data schema
2. IDE autocomplete support
3. Type checking with mypy
4. Reference for field names and types

ARCHITECTURE OVERVIEW
─────────────────────
We have 4 MongoDB collections, each representing a stage in the fixture lifecycle:

    fixtures_staging  →  fixtures_active  →  fixtures_completed
         │                    │
         └── fixtures_live ───┘
             (temporary comparison buffer)

FIXTURE LIFECYCLE
─────────────────
1. STAGING (fixtures_staging)
   - Fixtures scheduled for today with status NS (Not Started) or TBD
   - Waiting for match to begin
   - Contains raw API data only

2. ACTIVATION (staging → active)
   - Triggered when status changes from NS/TBD to any active status (1H, HT, 2H, etc.)
   - Enhanced fields are added (_activated_at, _last_activity, etc.)
   - Events array starts empty

3. ACTIVE (fixtures_active)
   - Match in progress, events being tracked
   - Events get debounced (3 consecutive polls) before confirmation
   - Twitter workflow searches for video clips
   - Contains both raw API data and our enhanced fields

4. LIVE (fixtures_live)
   - Temporary buffer for raw API poll data
   - Used for comparison with active to detect changes
   - Overwritten each poll cycle

5. COMPLETION (active → completed)
   - Triggered when status changes to FT, AET, PEN, etc.
   - Requires completion counter (3 polls) OR winner data populated
   - All events must be monitor_complete AND twitter_complete
"""

from datetime import datetime
from enum import Enum
from typing import Any, Dict, List, Optional, TypedDict


# =============================================================================
# ENUMS
# =============================================================================

class FixtureStatus(str, Enum):
    """
    All possible fixture statuses from API-Football.
    
    Grouped by lifecycle stage for easy filtering.
    """
    # Staging statuses (not started)
    TBD = "TBD"   # Time To Be Defined
    NS = "NS"     # Not Started
    
    # Active statuses (in progress)
    FIRST_HALF = "1H"
    HALFTIME = "HT"
    SECOND_HALF = "2H"
    EXTRA_TIME = "ET"
    BREAK_TIME = "BT"  # Break during extra time
    PENALTY = "P"       # Penalty shootout in progress
    SUSPENDED = "SUSP"
    INTERRUPTED = "INT"
    LIVE = "LIVE"       # In progress (generic)
    POSTPONED = "PST"   # Postponed - keep monitoring (may resume same day)
    
    # Completed statuses
    FULL_TIME = "FT"
    AFTER_EXTRA_TIME = "AET"
    PENALTIES = "PEN"
    
    # Cancelled/Postponed statuses (truly terminal)
    CANCELLED = "CANC"
    ABANDONED = "ABD"
    WALKOVER = "WO"      # Technical win
    AWARDED = "AWD"      # Technical win
    
    @classmethod
    def staging_statuses(cls) -> List[str]:
        """Statuses that belong in fixtures_staging."""
        return ["TBD", "NS"]
    
    @classmethod
    def active_statuses(cls) -> List[str]:
        """Statuses for matches in progress (including PST for short delays)."""
        return ["1H", "HT", "2H", "ET", "BT", "P", "SUSP", "INT", "LIVE", "PST"]
    
    @classmethod
    def completed_statuses(cls) -> List[str]:
        """Statuses for finished matches (ready for archive)."""
        return ["FT", "AET", "PEN", "CANC", "ABD", "WO", "AWD"]


class EventType(str, Enum):
    """
    Event types from API-Football.
    
    NOTE: This enum is for documentation/type hints only!
    The actual tracking logic is in src/utils/event_config.py which defines:
    - Which event types are enabled (currently only Goal)
    - Which event details are trackable (Normal Goal, Penalty, Own Goal)
    - Debounce settings per event type
    
    See EVENT_TYPES dict in event_config.py for the authoritative config.
    """
    GOAL = "Goal"
    CARD = "Card"
    SUBSTITUTION = "subst"
    VAR = "Var"
    
    @classmethod
    def trackable_types(cls) -> List[str]:
        """
        Event types we currently track.
        
        NOTE: For detailed filtering (which goal types, etc.), 
        see src/utils/event_config.py and should_track_event().
        """
        return ["Goal"]


# =============================================================================
# API DATA TYPES (Raw from API-Football)
# =============================================================================

class APIStatus(TypedDict, total=False):
    """Fixture status from API."""
    long: str
    short: str
    elapsed: Optional[int]
    extra: Optional[int]


class APIFixture(TypedDict, total=False):
    """Core fixture info from API."""
    id: int
    referee: Optional[str]
    timezone: str
    date: str  # ISO format
    timestamp: int
    status: APIStatus


class APIVenue(TypedDict, total=False):
    """Venue info from API."""
    id: Optional[int]
    name: Optional[str]
    city: Optional[str]


class APILeague(TypedDict, total=False):
    """League/competition info from API."""
    id: int
    name: str
    country: str
    logo: Optional[str]
    flag: Optional[str]
    season: int
    round: Optional[str]


class APITeam(TypedDict, total=False):
    """Team info from API."""
    id: int
    name: str
    logo: Optional[str]
    winner: Optional[bool]  # True=won, False=lost, None=draw/undetermined


class APITeams(TypedDict):
    """Home and away teams."""
    home: APITeam
    away: APITeam


class APIGoals(TypedDict, total=False):
    """Current score."""
    home: Optional[int]
    away: Optional[int]


class APIScore(TypedDict, total=False):
    """Score breakdown by period."""
    halftime: APIGoals
    fulltime: APIGoals
    extratime: Optional[APIGoals]
    penalty: Optional[APIGoals]


class APIPlayer(TypedDict, total=False):
    """Player info in events."""
    id: Optional[int]
    name: Optional[str]


class APIEventTime(TypedDict, total=False):
    """Event timing."""
    elapsed: Optional[int]
    extra: Optional[int]


class APIEventAssist(TypedDict, total=False):
    """Assist info for goals."""
    id: Optional[int]
    name: Optional[str]


class APIEvent(TypedDict, total=False):
    """
    Raw event from API-Football.
    
    We only track Goal events for video discovery, but the API returns
    all event types (cards, substitutions, VAR, etc.).
    """
    time: APIEventTime
    team: APITeam
    player: APIPlayer
    assist: Optional[APIEventAssist]
    type: str  # "Goal", "Card", "subst", "Var"
    detail: str  # "Normal Goal", "Penalty", "Yellow Card", etc.
    comments: Optional[str]


# =============================================================================
# FIELD CONSTANTS (for programmatic access)
# These MUST come before helper functions so they can be used
# =============================================================================

class FixtureFields:
    """
    Constants for fixture-level enhanced fields.
    
    Use these instead of string literals to avoid typos.
    
    Example:
        doc[FixtureFields.ACTIVATED_AT] = datetime.now()
    """
    ACTIVATED_AT = "_activated_at"
    LAST_ACTIVITY = "_last_activity"
    COMPLETION_COUNT = "_completion_count"
    COMPLETION_COMPLETE = "_completion_complete"
    COMPLETION_FIRST_SEEN = "_completion_first_seen"
    COMPLETED_AT = "_completed_at"
    
    @classmethod
    def all_enhanced(cls) -> List[str]:
        """All enhanced fixture fields (underscore-prefixed)."""
        return [
            cls.ACTIVATED_AT,
            cls.LAST_ACTIVITY,
            cls.COMPLETION_COUNT,
            cls.COMPLETION_COMPLETE,
            cls.COMPLETION_FIRST_SEEN,
            cls.COMPLETED_AT,
        ]


class EventFields:
    """
    Constants for event-level enhanced fields.
    
    Use these instead of string literals to avoid typos.
    
    Example:
        event[EventFields.MONITOR_COUNT] = 1
    """
    # Identification
    EVENT_ID = "_event_id"
    
    # Monitor tracking
    MONITOR_COUNT = "_monitor_count"
    MONITOR_COMPLETE = "_monitor_complete"
    FIRST_SEEN = "_first_seen"
    
    # Twitter tracking
    TWITTER_COUNT = "_twitter_count"
    TWITTER_COMPLETE = "_twitter_complete"
    TWITTER_COMPLETED_AT = "_twitter_completed_at"
    TWITTER_SEARCH = "_twitter_search"
    TWITTER_ALIASES = "_twitter_aliases"  # Team name aliases from RAG
    
    # Video storage
    DISCOVERED_VIDEOS = "_discovered_videos"
    S3_VIDEOS = "_s3_videos"
    VIDEO_COUNT = "_video_count"
    
    # Download workflow stats (for debugging/visibility)
    DOWNLOAD_STATS = "_download_stats"  # {dropped_no_hash, dropped_ai_rejected, etc.}
    
    # Score context
    SCORE_AFTER = "_score_after"
    SCORING_TEAM = "_scoring_team"
    
    # VAR
    REMOVED = "_removed"
    
    @classmethod
    def all_enhanced(cls) -> List[str]:
        """All enhanced event fields (underscore-prefixed)."""
        return [
            cls.EVENT_ID,
            cls.MONITOR_COUNT,
            cls.MONITOR_COMPLETE,
            cls.FIRST_SEEN,
            cls.TWITTER_COUNT,
            cls.TWITTER_COMPLETE,
            cls.TWITTER_COMPLETED_AT,
            cls.TWITTER_SEARCH,
            cls.TWITTER_ALIASES,
            cls.DISCOVERED_VIDEOS,
            cls.S3_VIDEOS,
            cls.VIDEO_COUNT,
            cls.DOWNLOAD_STATS,
            cls.SCORE_AFTER,
            cls.SCORING_TEAM,
            cls.REMOVED,
        ]


class VideoFields:
    """
    Constants for video object fields in _s3_videos.
    
    Example:
        video[VideoFields.POPULARITY] += 1
    """
    URL = "url"
    PERCEPTUAL_HASH = "perceptual_hash"
    RESOLUTION_SCORE = "resolution_score"
    FILE_SIZE = "file_size"
    POPULARITY = "popularity"
    RANK = "rank"


# =============================================================================
# DOWNLOAD STATS TYPE
# =============================================================================

class DownloadStats(TypedDict, total=False):
    """
    Statistics from DownloadWorkflow for debugging/visibility.
    
    Tracks what happened to videos at each stage of the pipeline.
    Stored in _download_stats field on events.
    
    PIPELINE FLOW:
    discovered → downloaded → md5_deduped → ai_rejected → hash_failed → perceptual_deduped → uploaded
    
    Each stage can reduce the count. The stats help diagnose issues like:
    - Too many ai_rejected: Search query finding wrong videos
    - Too many hash_failed: ffmpeg or resource issues  
    - Too many perceptual_deduped: Multiple sources posting same clip (good!)
    """
    # Input
    discovered: int              # Total discovered videos from Twitter
    
    # Download stage
    downloaded: int              # Successfully downloaded
    filtered_aspect_duration: int  # Filtered by aspect ratio or duration
    download_failed: int         # Failed to download (403, timeout, etc.)
    
    # MD5 dedup stage
    md5_deduped: int             # Removed by MD5 dedup (identical files in batch)
    md5_s3_matched: int          # MD5 matched existing S3 (popularity bumped)
    
    # AI validation stage
    ai_rejected: int             # Rejected by AI validation (not soccer)
    ai_validation_failed: int    # AI validation error (timeout, etc.)
    
    # Hash generation stage
    hash_generated: int          # Successfully generated perceptual hash
    hash_failed: int             # Perceptual hash generation failed
    
    # Perceptual dedup stage
    perceptual_deduped: int      # Removed by perceptual dedup (similar content)
    s3_replaced: int             # Replaced lower quality S3 videos
    s3_popularity_bumped: int    # Existing S3 kept, popularity bumped
    
    # Final output
    uploaded: int                # Successfully uploaded to S3


# =============================================================================
# S3 VIDEO TYPES
# =============================================================================

class S3Video(TypedDict, total=False):
    """
    Video stored in S3.
    
    These are deduplicated using perceptual hashing.
    Rank is calculated based on popularity and file size.
    """
    url: str              # Relative URL: /video/footy-videos/{key}
    _s3_key: str          # S3 key for direct operations
    perceptual_hash: str  # Hash for deduplication
    resolution_score: float
    file_size: int        # File size in bytes
    popularity: int       # Number of times this clip was found (default: 1)
    rank: int             # 1=best, higher=worse
    # Quality metadata
    width: int
    height: int
    aspect_ratio: float
    bitrate: int
    duration: float
    source_url: str       # Original tweet URL
    hash_version: str     # Version of hash algorithm used


class DiscoveredVideo(TypedDict, total=False):
    """
    Video found on Twitter before upload to S3.
    
    This is the raw video metadata from the Twitter scraper.
    """
    video_page_url: str   # Twitter video page URL
    video_url: str        # Direct video URL
    tweet_url: str        # Tweet containing the video
    tweet_text: str       # Tweet text
    username: str         # Twitter username
    views: int            # Video view count
    likes: int            # Tweet likes
    retweets: int         # Tweet retweets


# =============================================================================
# ENHANCED EVENT TYPE
# =============================================================================

class EnhancedEvent(TypedDict, total=False):
    """
    Event with our tracking/enhancement fields.
    
    Extends raw API event data with:
    - Unique identifier (_event_id)
    - Debounce tracking (_monitor_count, _monitor_complete)
    - Twitter workflow tracking (_twitter_count, _twitter_complete)
    - Video storage (_discovered_videos, _s3_videos)
    - Score context (_score_after, _scoring_team)
    
    DEBOUNCE PATTERN
    ────────────────
    Events must appear in 3 consecutive API polls before we consider them "real".
    This handles:
    - API delays (event appears, disappears, reappears)
    - VAR reviews (goal counted, then cancelled)
    - Minute drift (goal at 44' then 45')
    
    _monitor_count increments each poll while event is present.
    At count=3, _monitor_complete=True and Twitter workflow is triggered.
    
    TWITTER WORKFLOW
    ────────────────
    After monitor complete, we search Twitter for video clips.
    Up to 3 attempts (_twitter_count) spaced across polls.
    _twitter_complete=True when workflow finishes (success or max attempts).
    
    VAR HANDLING
    ────────────
    If event disappears from API (VAR cancelled), we DELETE it entirely.
    This is important because event_id uses player sequence numbers.
    Deleting frees the slot for the same player to score again.
    """
    # === Raw API Fields (from APIEvent) ===
    time: APIEventTime
    team: Dict[str, Any]
    player: Dict[str, Any]
    assist: Optional[Dict[str, Any]]
    type: str
    detail: str
    comments: Optional[str]
    
    # === Identification ===
    # Unique ID: {fixture_id}_{team_id}_{player_id}_{type}_{sequence}
    _event_id: str
    
    # === Monitor/Debounce Tracking ===
    _monitor_count: int       # Consecutive polls event has appeared (0-3)
    _monitor_complete: bool   # True when monitor_count >= 3 (debounce finished)
    _first_seen: datetime     # When event was first detected
    
    # === Twitter Workflow Tracking ===
    _twitter_count: int       # Twitter search attempts (0-3)
    _twitter_complete: bool   # True when Twitter workflow finished
    _twitter_completed_at: datetime
    _twitter_search: str      # Search query for Twitter
    _twitter_aliases: List[str]  # Team name aliases from RAG
    
    # === Video Storage ===
    _discovered_videos: List[DiscoveredVideo]  # Videos found on Twitter
    _s3_videos: List[S3Video]                  # Videos uploaded to S3
    _video_count: int                          # Total videos discovered
    
    # === Download Stats (debugging/visibility) ===
    _download_stats: DownloadStats             # Stats from DownloadWorkflow
    
    # === Score Context ===
    _score_after: str         # Score after this event (e.g., '2-1')
    _scoring_team: str        # Which team scored: 'home' or 'away'
    
    # === VAR Tracking ===
    _removed: bool            # True if event was VAR cancelled (then deleted)


# =============================================================================
# FIXTURE ENHANCEMENT TYPE
# =============================================================================

class FixtureEnhancement(TypedDict, total=False):
    """
    Enhanced fields added to fixtures in fixtures_active.
    
    These track the fixture through its lifecycle from activation to completion.
    
    ACTIVATION
    ──────────
    When a fixture moves from staging to active:
    - _activated_at: Set to current time
    - _last_activity: Set to activation time, updated on goal confirmations
    - _completion_count: 0
    - _completion_complete: False
    
    COMPLETION PATTERN
    ──────────────────
    Similar to event debouncing, fixtures must be "confirmed complete" before archiving.
    This handles:
    - Penalty shootouts (match ends but penalties continue)
    - API delays in winner determination
    
    Two conditions trigger completion:
    1. _completion_count >= 3 (3 consecutive polls showing completed status)
    2. Winner data populated (teams.home.winner or teams.away.winner is True)
    
    Additionally, all events must be _monitor_complete AND _twitter_complete.
    """
    _activated_at: datetime         # When fixture moved from staging to active
    _last_activity: datetime        # Set on activation, updated when goals confirmed
    _completion_count: int          # Consecutive polls showing completed status (0-3)
    _completion_complete: bool      # True when ready for completion move
    _completion_first_seen: datetime  # When completed status was first detected
    _completed_at: datetime         # When fixture moved to fixtures_completed


# =============================================================================
# COMPLETE FIXTURE TYPES
# =============================================================================

class StagingFixture(TypedDict, total=False):
    """
    Fixture in fixtures_staging collection.
    
    Contains only raw API data. No enhanced fields yet.
    Waiting for match to start (status: NS, TBD).
    """
    _id: int
    fixture: APIFixture
    league: APILeague
    teams: APITeams
    goals: APIGoals
    score: APIScore
    events: List[APIEvent]  # Usually empty


class ActiveFixture(TypedDict, total=False):
    """
    Fixture in fixtures_active collection.
    
    Contains raw API data PLUS enhanced tracking fields.
    Match in progress, events being debounced and processed.
    
    This is the most complex document structure because it contains:
    - All raw API data (fixture, teams, goals, etc.)
    - Fixture-level tracking (_activated_at, _last_activity, _completion_*)
    - Enhanced events with debounce and video tracking
    """
    _id: int
    fixture: APIFixture
    league: APILeague
    teams: APITeams
    goals: APIGoals
    score: APIScore
    events: List[EnhancedEvent]
    
    # Enhanced fixture-level fields
    _activated_at: datetime
    _last_activity: datetime
    _completion_count: int
    _completion_complete: bool
    _completion_first_seen: datetime


class CompletedFixture(TypedDict, total=False):
    """
    Fixture in fixtures_completed collection.
    
    Same structure as ActiveFixture but with _completed_at timestamp.
    All events have _monitor_complete=True and _twitter_complete=True.
    """
    _id: int
    fixture: APIFixture
    league: APILeague
    teams: APITeams
    goals: APIGoals
    score: APIScore
    events: List[EnhancedEvent]
    
    # All enhanced fields from active
    _activated_at: datetime
    _last_activity: datetime
    _completion_count: int
    _completion_complete: bool
    _completion_first_seen: datetime
    _completed_at: datetime


# =============================================================================
# HELPER FUNCTIONS
# =============================================================================

def create_event_id(fixture_id: int, team_id: int, player_id: int, event_type: str, sequence: int) -> str:
    """
    Generate a unique event ID.
    
    Format: {fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}
    
    Sequence is per player+type, allowing the same player to score multiple goals.
    When a goal is VAR'd, we DELETE the event entirely, freeing the sequence slot.
    
    Examples:
        - 1234567_100_200_Goal_1 (first goal by player 200)
        - 1234567_100_200_Goal_2 (second goal by player 200)
        - 1234567_100_0_Goal_1   (own goal, player_id=0)
    """
    return f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"


def create_new_enhanced_event(
    live_event: Dict[str, Any],
    event_id: str,
    twitter_search: str,
    score_after: str,
    scoring_team: str,
    initial_monitor_count: int = 1,
) -> EnhancedEvent:
    """
    Create an enhanced event dict for insertion into fixtures_active.
    
    This is called when a NEW event is detected (live has it, active doesn't).
    By default, the event starts with monitor_count=1 and will be debounced over 3 polls.
    
    For events with unknown players (player_id=0), pass initial_monitor_count=0.
    This signals to the frontend that the player is not yet identified,
    and prevents debouncing until the player is known.
    
    Uses EventFields constants for field names.
    
    Args:
        live_event: Raw event from API with _event_id already set
        event_id: The _event_id for this event
        twitter_search: Search query for Twitter
        score_after: Score after this event (e.g., "2-1")
        scoring_team: "home" or "away"
        initial_monitor_count: Starting monitor count (0 for unknown players, 1 for known)
    
    Returns:
        Enhanced event dict ready for MongoDB insertion
    """
    from datetime import datetime, timezone
    
    return {
        **live_event,
        # Identification
        EventFields.EVENT_ID: event_id,
        # Monitor tracking
        EventFields.MONITOR_COUNT: initial_monitor_count,
        EventFields.MONITOR_COMPLETE: False,
        EventFields.FIRST_SEEN: datetime.now(timezone.utc),
        # Twitter tracking
        EventFields.TWITTER_COUNT: 0,
        EventFields.TWITTER_COMPLETE: False,
        EventFields.TWITTER_SEARCH: twitter_search,
        # Video storage
        EventFields.DISCOVERED_VIDEOS: [],
        EventFields.S3_VIDEOS: [],
        # Score context
        EventFields.SCORE_AFTER: score_after,
        EventFields.SCORING_TEAM: scoring_team,
        # VAR
        EventFields.REMOVED: False,
    }


def create_activation_fields() -> FixtureEnhancement:
    """
    Create the enhanced fields for fixture activation.
    
    These fields are added when a fixture moves from staging to active.
    Uses FixtureFields constants for field names.
    
    Returns:
        Dict with _activated_at, _last_activity, _completion_count, _completion_complete
    """
    from datetime import datetime, timezone
    
    now = datetime.now(timezone.utc)
    return {
        FixtureFields.ACTIVATED_AT: now,
        FixtureFields.LAST_ACTIVITY: now,
        FixtureFields.COMPLETION_COUNT: 0,
        FixtureFields.COMPLETION_COMPLETE: False,
    }


# =============================================================================
# TEAM ALIAS TYPES (for RAG/Twitter search)
# =============================================================================

class TeamType(str, Enum):
    """
    Type of team for RAG processing.
    
    National teams get nationality adjectives added (e.g., "Brazilian", "French").
    Clubs get standard alias processing.
    """
    CLUB = "club"
    NATIONAL = "national"


class TeamAliasFields:
    """
    Constants for team_aliases collection fields.
    
    Use these instead of string literals to avoid typos.
    
    Example:
        doc[TeamAliasFields.TWITTER_ALIASES]
    """
    # _id is team_id (API-Football team ID)
    TEAM_NAME = "team_name"
    TEAM_TYPE = "team_type"       # "club" or "national" (string, legacy)
    NATIONAL = "national"          # True if national team, False if club (boolean, canonical)
    COUNTRY = "country"            # Country from API-Football (e.g., "England")
    CITY = "city"                  # City from venue data (e.g., "Newcastle upon Tyne")
    TWITTER_ALIASES = "twitter_aliases"
    MODEL = "model"
    WIKIDATA_QID = "wikidata_qid"
    WIKIDATA_ALIASES = "wikidata_aliases"
    CREATED_AT = "created_at"
    UPDATED_AT = "updated_at"
    
    @classmethod
    def all_fields(cls) -> List[str]:
        """All team alias document fields."""
        return [
            cls.TEAM_NAME,
            cls.TEAM_TYPE,
            cls.NATIONAL,
            cls.COUNTRY,
            cls.CITY,
            cls.TWITTER_ALIASES,
            cls.MODEL,
            cls.WIKIDATA_QID,
            cls.WIKIDATA_ALIASES,
            cls.CREATED_AT,
            cls.UPDATED_AT,
        ]


class TeamAlias(TypedDict, total=False):
    """
    Team alias document in team_aliases collection.
    
    Stores Twitter-friendly aliases for teams, generated via RAG pipeline:
    1. Fetch aliases from Wikidata
    2. Preprocess to single words
    3. LLM selects best words for Twitter search
    4. Add team name words and nationality adjectives
    
    The _id is the API-Football team_id for O(1) cache lookups.
    The 'national' boolean is the canonical source of truth for team type.
    
    Example document:
    {
        "_id": 40,  # Liverpool's team_id
        "team_name": "Liverpool",
        "team_type": "club",
        "national": false,
        "twitter_aliases": ["LFC", "Reds", "Anfield", "Liverpool"],
        "model": "qwen3-vl:8b-instruct",
        "wikidata_qid": "Q1130849",
        "wikidata_aliases": ["Liverpool F.C.", "Liverpool Football Club", "LFC", ...],
        "created_at": "2025-12-26T10:30:00Z",
        "updated_at": "2025-12-26T10:30:00Z"
    }
    """
    _id: int                      # API-Football team_id (cache key)
    team_name: str                # Full team name from API
    team_type: str                # "club" or "national" (legacy, derived from national)
    national: bool                # True if national team, False if club (canonical)
    twitter_aliases: List[str]   # Final aliases for Twitter search
    model: str                    # LLM model used (or "fallback")
    wikidata_qid: Optional[str]  # Wikidata QID (e.g., "Q1130849")
    wikidata_aliases: Optional[List[str]]  # Raw aliases from Wikidata
    created_at: datetime
    updated_at: datetime


def create_team_alias(
    team_id: int,
    team_name: str,
    national: bool,
    twitter_aliases: List[str],
    model: str,
    wikidata_qid: Optional[str] = None,
    wikidata_aliases: Optional[List[str]] = None,
) -> TeamAlias:
    """
    Create a team alias document for insertion into team_aliases collection.
    
    Args:
        team_id: API-Football team ID (used as _id)
        team_name: Full team name
        national: True if national team, False if club (from API)
        twitter_aliases: Final aliases for Twitter search
        model: LLM model used (or "fallback", "heuristic")
        wikidata_qid: Wikidata QID if found
        wikidata_aliases: Raw aliases from Wikidata
    
    Returns:
        TeamAlias dict ready for MongoDB upsert
    """
    from datetime import datetime, timezone
    
    now = datetime.now(timezone.utc)
    team_type = "national" if national else "club"  # Legacy field derived from national
    
    doc: TeamAlias = {
        "_id": team_id,
        TeamAliasFields.TEAM_NAME: team_name,
        TeamAliasFields.TEAM_TYPE: team_type,
        TeamAliasFields.NATIONAL: national,
        TeamAliasFields.TWITTER_ALIASES: twitter_aliases,
        TeamAliasFields.MODEL: model,
        TeamAliasFields.CREATED_AT: now,
        TeamAliasFields.UPDATED_AT: now,
    }
    
    if wikidata_qid:
        doc[TeamAliasFields.WIKIDATA_QID] = wikidata_qid
    if wikidata_aliases:
        doc[TeamAliasFields.WIKIDATA_ALIASES] = wikidata_aliases
    
    return doc
