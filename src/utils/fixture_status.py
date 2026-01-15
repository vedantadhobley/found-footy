"""Fixture status management - pure Python, no orchestration dependencies"""

# FIFA STATUS DEFINITIONS - Centralized and documented
# 
# IMPORTANT: PST (Postponed) is treated as ACTIVE, not completed!
# This is because short delays (15-30 min) are common and the match may still
# happen the same day. Once PST, we keep monitoring until:
# - Match starts (1H)
# - Match is cancelled (CANC) 
# - Match is truly rescheduled to another day (manual archive or next day cleanup)
#
FIXTURE_STATUSES = {
    "completed": {
        "FT": "Match Finished (regular time)",
        "AET": "Match Finished (after extra time)",
        "PEN": "Match Finished (after penalty shootout)",
        # PST removed - now treated as active to handle short delays
        "CANC": "Match Cancelled (will not be played)",
        "ABD": "Match Abandoned (may not be rescheduled)",
        "AWD": "Technical Loss (awarded result)",
        "WO": "WalkOver (forfeit)"
    },
    "active": {
        "1H": "First Half in progress",
        "HT": "Halftime (will resume)",
        "2H": "Second Half in progress", 
        "ET": "Extra Time in progress",
        "BT": "Break Time (between periods)",
        "P": "Penalty Shootout (wait for completion)",
        "SUSP": "Match Suspended (may resume)",
        "INT": "Match Interrupted (should resume)",
        "LIVE": "Generic live status",
        "PST": "Match Postponed (keep monitoring - may resume same day)"
    },
    "staging": {
        "TBD": "Time To Be Defined (pre-match)",
        "NS": "Not Started (pre-match)"
    }
}

def get_fixture_statuses():
    """Get fixture status configuration"""
    return {
        "completed": list(FIXTURE_STATUSES["completed"].keys()),
        "active": list(FIXTURE_STATUSES["active"].keys()),
        "staging": list(FIXTURE_STATUSES["staging"].keys()),
        "all_descriptions": FIXTURE_STATUSES
    }

def get_completed_statuses():
    """Get list of completed status codes"""
    return list(FIXTURE_STATUSES["completed"].keys())

def get_active_statuses():
    """Get list of active status codes"""
    return list(FIXTURE_STATUSES["active"].keys())

def get_staging_statuses():
    """Get list of staging status codes"""
    return list(FIXTURE_STATUSES["staging"].keys())

def is_fixture_completed(status_code):
    """Check if a status code indicates completion"""
    return status_code in get_completed_statuses()

def is_fixture_active(status_code):
    """Check if a status code indicates active monitoring"""
    return status_code in get_active_statuses()

def is_fixture_staging(status_code):
    """Check if a status code indicates staging"""
    return status_code in get_staging_statuses()
