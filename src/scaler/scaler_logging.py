"""
Structured logging for the Scaler service.

Produces JSON logs for Grafana Loki dashboarding.
Same format as the main logging module but standalone for the scaler container.
"""
import json
import os
import sys
from datetime import datetime, timezone


class ScalerLogger:
    """Structured JSON logger for the scaler service."""
    
    def __init__(self):
        self.pretty = os.environ.get("LOG_FORMAT", "").lower() == "pretty"
    
    def _emit(self, level: str, module: str, action: str, msg: str, **kwargs):
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
            # Human-readable format for local dev
            extras = " | ".join(f"{k}={v}" for k, v in kwargs.items()) if kwargs else ""
            output = f"{entry['ts'][:23]} | {level:5} | {module} | {action} | {msg}"
            if extras:
                output += f" | {extras}"
            print(output, flush=True)
        else:
            # JSON for production/Loki
            print(json.dumps(entry, default=str), flush=True)
    
    def debug(self, module: str, action: str, msg: str, **kwargs):
        self._emit("DEBUG", module, action, msg, **kwargs)
    
    def info(self, module: str, action: str, msg: str, **kwargs):
        self._emit("INFO", module, action, msg, **kwargs)
    
    def warning(self, module: str, action: str, msg: str, **kwargs):
        self._emit("WARN", module, action, msg, **kwargs)
    
    def error(self, module: str, action: str, msg: str, **kwargs):
        self._emit("ERROR", module, action, msg, **kwargs)


# Singleton instance
log = ScalerLogger()
