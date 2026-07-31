"""Internal config resolution helpers shared by the sync and async clients.

Precedence for both fields is: explicit constructor argument > environment
variable. Neither the API key nor any derived value is ever logged or
included in exception messages verbatim — see :func:`redact_key`.
"""

from __future__ import annotations

import os
from typing import Optional

TRIDENT_API_KEY_ENV = "TRIDENT_API_KEY"
TRIDENT_BASE_URL_ENV = "TRIDENT_BASE_URL"


class TridentConfigError(ValueError):
    """Raised when required client configuration is missing or invalid."""


def resolve_api_key(api_key: Optional[str]) -> str:
    """Resolve the API key from an explicit value or TRIDENT_API_KEY.

    Raises:
        TridentConfigError: if neither source provides a non-empty key.
    """
    resolved = api_key or os.environ.get(TRIDENT_API_KEY_ENV, "")
    if not resolved:
        raise TridentConfigError(
            "Trident API key is required: pass api_key= explicitly or set "
            f"the {TRIDENT_API_KEY_ENV} environment variable."
        )
    return resolved


def resolve_api_url(api_url: Optional[str]) -> str:
    """Resolve the base URL from an explicit value or TRIDENT_BASE_URL.

    Raises:
        TridentConfigError: if neither source provides a non-empty URL.
    """
    resolved = api_url or os.environ.get(TRIDENT_BASE_URL_ENV, "")
    if not resolved:
        raise TridentConfigError(
            "Trident api_url is required: pass api_url= explicitly or set "
            f"the {TRIDENT_BASE_URL_ENV} environment variable."
        )
    return resolved.rstrip("/")


def redact_key(key: str) -> str:
    """Return a redacted form of an API key, safe to log or print."""
    if not key:
        return "<empty>"
    if len(key) <= 4:
        return "***"
    return f"***{key[-4:]}"
