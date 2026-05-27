"""HTTP exception classes for consistent JSON error shapes.

FastAPI's default `HTTPException` produces `{"detail": "..."}`. We use
`{"error": "<machine-readable code>", "message": "<human readable>", ...}`
as the standard shape so client code can switch on `body.error` without
parsing free-form strings.
"""

from typing import Any, Dict, Optional

from fastapi import HTTPException


def _shape(error_code: str, message: str, **extra: Any) -> Dict[str, Any]:
    body: Dict[str, Any] = {"error": error_code, "message": message}
    body.update(extra)
    return body


class NotFoundError(HTTPException):
    def __init__(self, resource: str, identifier: Optional[str] = None):
        super().__init__(
            status_code=404,
            detail=_shape("not_found", f"{resource} not found", resource=resource, id=identifier),
        )


class ServiceUnavailableError(HTTPException):
    def __init__(self, message: str = "Service temporarily unavailable", **extra: Any):
        super().__init__(
            status_code=503,
            detail=_shape("service_unavailable", message, **extra),
        )


class BadRequestError(HTTPException):
    def __init__(self, message: str, **extra: Any):
        super().__init__(
            status_code=400,
            detail=_shape("bad_request", message, **extra),
        )


class UnauthorizedError(HTTPException):
    def __init__(self, message: str = "Unauthorized"):
        super().__init__(
            status_code=401,
            detail=_shape("unauthorized", message),
        )
