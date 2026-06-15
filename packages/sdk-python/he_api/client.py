"""Story 10.2 — drop-in He-API clients (R-OQ-1: subclass, override only __init__).

``Client`` / ``AsyncClient`` subclass the official ``openai.OpenAI`` /
``openai.AsyncOpenAI``. The ONLY behaviour we change is credential + endpoint
resolution in ``__init__``:

  * ``base_url``: explicit arg > ``HE_API_BASE_URL`` env > ``DEFAULT_BASE_URL``.
  * ``api_key``:  explicit arg > ``HE_API_KEY`` env. ``OPENAI_API_KEY`` is NEVER
    read (key isolation, BR-10.2.7) — if no He key resolves we raise the native
    ``openai.OpenAIError`` ourselves so the upstream OPENAI_API_KEY fallback is
    never reached.

Two thin convenience methods wrap the non-OpenAI gateway endpoints
``/v1/balance`` and ``/v1/usage`` (R-OQ-4c — the only genuinely new code). They
forward through the inherited openai escape-hatch and return parsed JSON; query
validation is the gateway's job (fail-loud), not duplicated here (BR-10.2.10).
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from typing import Any

import httpx
import openai

from ._config import DEFAULT_BASE_URL, HE_API_BASE_URL_ENV, HE_API_KEY_ENV

_MISSING_KEY_MESSAGE = (
    "The api_key client option must be set either by passing api_key to the "
    "client or by setting the HE_API_KEY environment variable"
)


def _resolve_base_url(explicit: str | httpx.URL | None) -> str | httpx.URL:
    """ctor > HE_API_BASE_URL env > DEFAULT_BASE_URL (BR-10.2.7)."""
    if explicit is not None:
        return explicit
    return os.environ.get(HE_API_BASE_URL_ENV) or DEFAULT_BASE_URL


def _resolve_api_key(explicit: str | None) -> str:
    """ctor > HE_API_KEY env; OPENAI_API_KEY is never consulted (BR-10.2.7).

    An empty string is treated as missing (so we never send a bare
    ``Authorization: Bearer`` header — BLIND-BOUNDARY-002). When nothing
    resolves we raise the SAME native ``openai.OpenAIError`` the upstream SDK
    would, but BEFORE it can fall back to ``OPENAI_API_KEY``.
    """
    key = explicit if explicit is not None else os.environ.get(HE_API_KEY_ENV)
    if not key:  # None or "" → missing
        raise openai.OpenAIError(_MISSING_KEY_MESSAGE)
    return key


def _balance_options(currency: str | None) -> dict[str, Any]:
    if currency is None:
        return {}
    return {"params": {"currency": currency}}


class Client(openai.OpenAI):
    """Drop-in synchronous He-API client (subclass of ``openai.OpenAI``)."""

    def __init__(
        self,
        *,
        api_key: str | None = None,
        base_url: str | httpx.URL | None = None,
        **kwargs: Any,
    ) -> None:
        super().__init__(
            api_key=_resolve_api_key(api_key),
            base_url=_resolve_base_url(base_url),
            **kwargs,
        )

    def get_balance(self, *, currency: str | None = None) -> Any:
        """GET /v1/balance via the inherited escape-hatch; returns parsed JSON.

        ``currency`` (∈ {usd, rmb}) is forwarded as ``?currency=`` and validated
        by the gateway (fail-loud) — the SDK does not re-validate (BR-10.2.10).
        """
        resp = self.get("/balance", cast_to=httpx.Response, options=_balance_options(currency))
        return resp.json()

    def get_usage(self, *, params: Mapping[str, Any] | None = None) -> Any:
        """GET /v1/usage via the inherited escape-hatch; returns parsed JSON."""
        options: dict[str, Any] = {"params": dict(params)} if params else {}
        resp = self.get("/usage", cast_to=httpx.Response, options=options)
        return resp.json()


class AsyncClient(openai.AsyncOpenAI):
    """Drop-in asynchronous He-API client (subclass of ``openai.AsyncOpenAI``)."""

    def __init__(
        self,
        *,
        api_key: str | None = None,
        base_url: str | httpx.URL | None = None,
        **kwargs: Any,
    ) -> None:
        super().__init__(
            api_key=_resolve_api_key(api_key),
            base_url=_resolve_base_url(base_url),
            **kwargs,
        )

    async def get_balance(self, *, currency: str | None = None) -> Any:
        """Async GET /v1/balance; returns parsed JSON (see ``Client.get_balance``)."""
        resp = await self.get(
            "/balance", cast_to=httpx.Response, options=_balance_options(currency)
        )
        return resp.json()

    async def get_usage(self, *, params: Mapping[str, Any] | None = None) -> Any:
        """Async GET /v1/usage; returns parsed JSON."""
        options: dict[str, Any] = {"params": dict(params)} if params else {}
        resp = await self.get("/usage", cast_to=httpx.Response, options=options)
        return resp.json()
