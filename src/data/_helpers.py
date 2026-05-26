"""
Internal helpers for src/data/ — module-level logging + PyMongo exception
classification used by all the mixin files in this package.

Kept separate from data/store.py so each mixin can do
`from src.data._helpers import _log_error, _log_info, _log_warning`
without importing the base class (avoids circular imports).

Phase 1 (P1d) auto-tags every _log_error call with error_category="mongo"
and — when the caller passes exc= — error_class + mongo_failure_type
extracted from PyMongo's typed exceptions. Existing call sites that
pass only error=str(e) keep working.
"""

from typing import Any, Dict, Optional

from src.utils.footy_logging import log, get_fallback_logger

MODULE = "mongo_store"


def _log_info(action: str, msg: str, **kwargs):
    """Log info using fallback logger."""
    log.info(get_fallback_logger(), MODULE, action, msg, **kwargs)


def _log_warning(action: str, msg: str, **kwargs):
    """Log warning using fallback logger."""
    log.warning(get_fallback_logger(), MODULE, action, msg, **kwargs)


# Map known PyMongo exception classes to coarse Phase 1 categories.
_PYMONGO_TRANSIENT_TYPES = {
    "NetworkTimeout",
    "AutoReconnect",
    "ServerSelectionTimeoutError",
    "ConnectionFailure",
    "WaitQueueTimeoutError",
    "WriteConcernError",
}
_PYMONGO_CONFLICT_TYPES = {
    "DuplicateKeyError",
    "OperationFailure",
}


def _classify_pymongo_exc(exc: Optional[BaseException]) -> Dict[str, str]:
    """Extract structured fields about a PyMongo exception for log enrichment."""
    if exc is None:
        return {}
    cls_name = type(exc).__name__
    if cls_name in _PYMONGO_TRANSIENT_TYPES:
        kind = "transient"
    elif cls_name in _PYMONGO_CONFLICT_TYPES:
        kind = "conflict"
    else:
        kind = "other"
    return {"error_class": cls_name, "mongo_failure_type": kind}


def _log_error(action: str, msg: str, *, exc: Optional[BaseException] = None, **kwargs):
    """Log error with Phase 1 enrichment (error_category=mongo + PyMongo classification)."""
    error_str = kwargs.pop("error", "")
    enrichment = _classify_pymongo_exc(exc)
    log.error(
        get_fallback_logger(),
        MODULE,
        action,
        msg,
        error_category="mongo",
        error=error_str,
        **enrichment,
        **kwargs,
    )
