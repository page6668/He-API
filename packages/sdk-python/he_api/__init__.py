"""He-API Python SDK — a drop-in, OpenAI-compatible client for the He-API gateway.

The He-API gateway speaks the OpenAI wire protocol, so "interface identity" is
achieved by **inheritance, not rewrite** (Story 10.2, R-OQ-1): ``Client`` /
``AsyncClient`` subclass ``openai.OpenAI`` / ``openai.AsyncOpenAI`` and override
only ``__init__`` to inject He-API defaults (gateway base_url, ``HE_API_KEY``).
Everything else — ``.chat`` / ``.embeddings`` / ``.models`` / ``.audio``, SSE
streaming, pydantic response models, retries, ``.with_raw_response`` — is
inherited unchanged.

    from he_api import Client
    client = Client()                       # base_url -> gateway, key <- HE_API_KEY
    client.chat.completions.create(model="qwen-max", messages=[...])

``__version__`` is the single source of truth for the package version: hatchling
reads it from this module (``[tool.hatch.version]``), so the installed
distribution metadata and ``he_api.__version__`` never drift (BR-10.2.4).
"""

from __future__ import annotations

# Single source of truth for the package version (BR-10.2.4 / R-OQ-6). hatchling
# reads this value as the distribution version; tests assert it equals
# importlib.metadata.version("he-api").
__version__ = "0.1.0"

# Re-export the OpenAI exception hierarchy so callers can write
# ``except he_api.BadRequestError`` (BR-10.2.8). Existing code written against
# ``openai.X`` keeps working unchanged because responses/errors ARE openai's own
# pydantic models / exception instances — inheritance, not re-wrapping.
from openai import (
    APIConnectionError,
    APIError,
    APIResponseValidationError,
    APIStatusError,
    APITimeoutError,
    AuthenticationError,
    BadRequestError,
    ConflictError,
    InternalServerError,
    NotFoundError,
    OpenAIError,
    PermissionDeniedError,
    RateLimitError,
    UnprocessableEntityError,
)

from ._config import DEFAULT_BASE_URL, HE_API_BASE_URL_ENV, HE_API_KEY_ENV
from .client import AsyncClient, Client

__all__ = [
    "__version__",
    "Client",
    "AsyncClient",
    "DEFAULT_BASE_URL",
    "HE_API_KEY_ENV",
    "HE_API_BASE_URL_ENV",
    # Re-exported OpenAI exception hierarchy (BR-10.2.8).
    "OpenAIError",
    "APIError",
    "APIStatusError",
    "APIConnectionError",
    "APITimeoutError",
    "APIResponseValidationError",
    "BadRequestError",
    "AuthenticationError",
    "PermissionDeniedError",
    "NotFoundError",
    "ConflictError",
    "UnprocessableEntityError",
    "RateLimitError",
    "InternalServerError",
]
