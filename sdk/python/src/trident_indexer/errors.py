"""Exceptions raised by the Trident SDK."""

from __future__ import annotations

import json
from typing import Optional


class TridentApiError(Exception):
    """Raised on all non-2xx responses from the Trident API."""

    def __init__(
        self,
        status: int,
        code: str,
        message: str,
        field: Optional[str] = None,
        attempts: int = 1,
    ) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.field = field
        #: Number of attempts made before this error was raised (>1 if retried).
        self.attempts = attempts

    def __repr__(self) -> str:  # pragma: no cover
        return f"TridentApiError(status={self.status}, code={self.code!r}, message={str(self)!r})"

    @classmethod
    def from_response(
        cls, status: int, body: str, attempts: int = 1
    ) -> "TridentApiError":
        """Parse a non-2xx response body into a TridentApiError."""
        try:
            parsed = json.loads(body)
            err = parsed.get("error", {})
            if isinstance(err, dict):
                return cls(
                    status=status,
                    code=err.get("code", "INTERNAL"),
                    message=err.get("message", body or f"HTTP {status}"),
                    field=err.get("field"),
                    attempts=attempts,
                )
        except (json.JSONDecodeError, AttributeError):
            pass
        return cls(
            status=status,
            code="INTERNAL",
            message=body or f"HTTP {status}",
            attempts=attempts,
        )
