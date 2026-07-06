"""
Structured JSON Logging for Twitter Service

Provides consistent, queryable logs for Grafana Loki dashboarding.
Mirrors the pattern used in Temporal workflows but works standalone.

Log Format:
{
    "ts": "2025-02-05T14:30:22.123456",
    "level": "INFO", 
    "module": "twitter_session",
    "action": "search_complete",
    "msg": "Search complete",
    "videos_found": 5,
    ...context fields
}

Usage:
    from twitter.twitter_logging import log
    
    log.info("twitter_session", "search_start", "Starting search", query=query)
    log.error("twitter_session", "search_failed", "Search failed", error=str(e))
"""
import json
import sys
import os
from datetime import datetime, timezone


class TwitterLogger:
    """Structured JSON logger for Twitter service."""
    
    def __init__(self):
        self.pretty = os.environ.get("LOG_FORMAT", "").lower() == "pretty"
        # Force unbuffered output
        sys.stdout.reconfigure(line_buffering=True)
        sys.stderr.reconfigure(line_buffering=True)
    
    def _log(self, level: str, module: str, action: str, msg: str, **kwargs):
        """Emit a structured log entry."""
        entry = {
            "ts": datetime.now(timezone.utc).isoformat(),
            "level": level,
            "module": module,
            "action": action,
            "msg": msg,
            **kwargs
        }
        
        if self.pretty:
            # Human-readable format for local development
            context = " | ".join(f"{k}={v}" for k, v in kwargs.items()) if kwargs else ""
            output = f"{entry['ts'][:23]} | {level:5} | {module} | {action} | {msg}"
            if context:
                output += f" | {context}"
            print(output, flush=True)
        else:
            # JSON format for Loki ingestion
            print(json.dumps(entry, default=str), flush=True)
    
    def info(self, module: str, action: str, msg: str, **kwargs):
        """Log INFO level message."""
        self._log("INFO", module, action, msg, **kwargs)
    
    def warning(self, module: str, action: str, msg: str, **kwargs):
        """Log WARNING level message."""
        self._log("WARN", module, action, msg, **kwargs)
    
    def error(self, module: str, action: str, msg: str, **kwargs):
        """Log ERROR level message."""
        self._log("ERROR", module, action, msg, **kwargs)
    
    def debug(self, module: str, action: str, msg: str, **kwargs):
        """Log DEBUG level message."""
        self._log("DEBUG", module, action, msg, **kwargs)


# Singleton instance
log = TwitterLogger()
