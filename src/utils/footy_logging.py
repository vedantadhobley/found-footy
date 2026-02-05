"""
Structured Logging for Grafana Loki

All logs are JSON with consistent, queryable fields.
Query logs by: module, action, event_id, fixture_id, etc.

GRAFANA LOKI QUERIES
====================
# All errors
{app="found-footy"} | json | level="ERROR"

# Download failures for specific event
{app="found-footy"} | json | module="download" action="download_failed" event_id="12345_40_234_Goal_1"

# All workflow starts
{app="found-footy"} | json | action="started"

# Monitor cycle metrics
{app="found-footy"} | json | module="monitor" action="cycle_complete"

# Video pipeline tracking
{app="found-footy"} | json | action=~"downloaded|validated|uploaded|rejected"

USAGE
=====
from src.utils.footy_logging import log

# In activities (pass activity.logger):
log.info(activity.logger, "download", "downloaded", "Video downloaded",
         event_id=event_id, video_idx=0, duration_ms=1234, file_size=5000000)

log.error(activity.logger, "download", "download_failed", "Download failed",
          event_id=event_id, error=str(e), error_type="TimeoutError")

# In workflows (pass workflow.logger):
log.info(workflow.logger, "twitter", "started", "TwitterWorkflow started",
         event_id=event_id, team_id=40)
"""

import json
import logging
import os
import re
import sys
from datetime import datetime, timezone
from typing import Any, Optional, Union


class StructuredFormatter(logging.Formatter):
    """JSON formatter for Grafana Loki. Strips Temporal context dicts."""
    
    TEMPORAL_CONTEXT = re.compile(r"\s*\(\{'.+\}\)\s*$")
    
    def __init__(self, pretty: bool = False):
        super().__init__()
        self.pretty = pretty
    
    def format(self, record: logging.LogRecord) -> str:
        msg = record.getMessage()
        msg = self.TEMPORAL_CONTEXT.sub("", msg)
        
        # Check for structured log
        if hasattr(record, "_structured") and record._structured:
            data = {
                "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
                "level": record.levelname,
                "module": record._module,
                "action": record._action,
                "msg": msg,
            }
            # Add extra fields
            for key, value in record._extra.items():
                if value is not None:
                    data[key] = value
            
            if self.pretty:
                return self._pretty(data)
            return json.dumps(data, default=str, separators=(',', ':'))
        
        # Legacy log - wrap in JSON
        if self.pretty:
            return msg
        return json.dumps({
            "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
            "level": record.levelname,
            "module": "legacy",
            "action": "log",
            "msg": msg,
        }, default=str, separators=(',', ':'))
    
    def _pretty(self, data: dict) -> str:
        """Human-readable format for development."""
        lvl = data["level"][0]  # I/W/E/D
        mod = data["module"].upper()[:8].ljust(8)
        act = data["action"]
        msg = data["msg"]
        
        # Build context
        skip = {"ts", "level", "module", "action", "msg"}
        ctx = " ".join(f"{k}={v}" for k, v in data.items() if k not in skip)
        
        return f"{lvl} [{mod}] {act}: {msg}" + (f" | {ctx}" if ctx else "")


class StructuredLogger:
    """
    Centralized structured logging.
    
    All methods accept a logger (activity.logger or workflow.logger),
    module name, action name, message, and arbitrary context fields.
    """
    
    def _log(
        self,
        logger: logging.Logger,
        level: int,
        module: str,
        action: str,
        msg: str,
        **kwargs
    ) -> None:
        """Emit a structured log."""
        # Create record with structured data
        extra = {
            "_structured": True,
            "_module": module,
            "_action": action,
            "_extra": {k: v for k, v in kwargs.items() if v is not None},
        }
        logger.log(level, msg, extra=extra)
    
    def info(
        self,
        logger: logging.Logger,
        module: str,
        action: str,
        msg: str,
        **kwargs
    ) -> None:
        """
        Log INFO level.
        
        Args:
            logger: activity.logger or workflow.logger
            module: Source module (download, upload, monitor, twitter, ingest, rag)
            action: Action name (started, completed, failed, downloaded, etc.)
            msg: Human-readable message
            **kwargs: Context fields (event_id, fixture_id, video_idx, etc.)
        """
        self._log(logger, logging.INFO, module, action, msg, **kwargs)
    
    def warning(
        self,
        logger: logging.Logger,
        module: str,
        action: str,
        msg: str,
        **kwargs
    ) -> None:
        """Log WARNING level."""
        self._log(logger, logging.WARNING, module, action, msg, **kwargs)
    
    def error(
        self,
        logger: logging.Logger,
        module: str,
        action: str,
        msg: str,
        error: Optional[str] = None,
        error_type: Optional[str] = None,
        **kwargs
    ) -> None:
        """
        Log ERROR level.
        
        Args:
            logger: activity.logger or workflow.logger
            module: Source module
            action: Action name (typically *_failed)
            msg: Human-readable error description
            error: Error message string
            error_type: Exception class name
            **kwargs: Additional context
        """
        self._log(
            logger, logging.ERROR, module, action, msg,
            error=error, error_type=error_type, **kwargs
        )
    
    def debug(
        self,
        logger: logging.Logger,
        module: str,
        action: str,
        msg: str,
        **kwargs
    ) -> None:
        """Log DEBUG level."""
        self._log(logger, logging.DEBUG, module, action, msg, **kwargs)


# Singleton instance
log = StructuredLogger()

# Fallback logger for infrastructure code (mongo_store, api_client, etc.)
# that doesn't have access to activity/workflow loggers
_fallback_logger = None


def get_fallback_logger() -> logging.Logger:
    """Get a fallback logger for infrastructure code without activity/workflow context."""
    global _fallback_logger
    if _fallback_logger is None:
        _fallback_logger = logging.getLogger("found-footy.infra")
    return _fallback_logger


def configure_logging():
    """Configure root logger with structured formatter. Call once at startup."""
    pretty = os.environ.get("LOG_FORMAT", "json") == "pretty"
    
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(StructuredFormatter(pretty=pretty))
    
    logging.basicConfig(
        level=logging.INFO,
        handlers=[handler],
        force=True,
    )
    
    # Temporal loggers
    logging.getLogger("temporalio.activity").setLevel(logging.INFO)
    logging.getLogger("temporalio.workflow").setLevel(logging.INFO)
    
    # Quiet noisy loggers
    logging.getLogger("temporalio.worker").setLevel(logging.WARNING)
    logging.getLogger("temporalio.client").setLevel(logging.WARNING)
    logging.getLogger("urllib3").setLevel(logging.WARNING)
    logging.getLogger("pymongo").setLevel(logging.WARNING)

