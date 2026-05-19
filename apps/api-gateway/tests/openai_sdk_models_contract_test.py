"""Story 3.5 — OpenAI Python SDK contract tests for GET /v1/models.

Source: docs/qa/assessments/3.5-test-design-20260519.md

Reason: confirms the gateway's /v1/models response shape parses cleanly
under the OpenAI SDK's Pydantic-strict `Model` class — a class of bug no
Go-side test can ever surface. Reuses the Story-3.3 SDK pin (`openai==1.40.*`)
and the env-var convention (`HE_API_TEST_GATEWAY_URL` + `HE_API_TEST_API_KEY`).

Skipped when env is unset (Story 3.3 BR-3.1 convention). CI runs them in
the gateway job after booting the api-gateway with miniredis / Postgres /
auth-svc stubs and minting a key via `scripts/issue-test-key.sh`.

Architect Round 1 OQ1 ruling: the canonical-id set MUST contain 11 entries
(NOT 9). qwen-plus + doubao-lite are required.
"""

import os

import openai
import pytest

GATEWAY_URL_ENV = "HE_API_TEST_GATEWAY_URL"
API_KEY_ENV = "HE_API_TEST_API_KEY"

_skip_reason = (
    f"set {GATEWAY_URL_ENV} + {API_KEY_ENV} to run the OpenAI SDK contract test"
)
_skip_if_no_gateway = pytest.mark.skipif(
    os.environ.get(GATEWAY_URL_ENV) is None
    or os.environ.get(API_KEY_ENV) is None,
    reason=_skip_reason,
)

# Architect Round 1 OQ1 — 11-entry canonical catalogue. Update T1.3
# (models.go modelsCatalogue declaration) AND this set together.
CANONICAL_MODEL_IDS = {
    "qwen-max",
    "qwen-plus",            # Architect OQ1 expansion — Epic 4.2
    "deepseek-v3",
    "moonshot-v1-128k",
    "glm-4",
    "doubao-pro",
    "doubao-lite",          # Architect OQ1 expansion — Epic 4.5
    "ernie-4.0",
    "he-router-cost",
    "he-router-quality",
    "he-router-latency",
}

CANONICAL_OWNED_BY = {
    "alibaba",
    "deepseek",
    "moonshot",
    "zhipu",
    "bytedance",
    "baidu",
    "he-api",
}


def _client(api_key: str | None = None) -> openai.OpenAI:
    """Construct a real OpenAI client pointed at the gateway under test."""
    return openai.OpenAI(
        base_url=os.environ[GATEWAY_URL_ENV].rstrip("/") + "/v1",
        api_key=api_key if api_key is not None else os.environ[API_KEY_ENV],
    )


# Scenario: 3.5-E2E-001
# Priority: P0
@_skip_if_no_gateway
def test_models_list_canonical_catalogue():
    """client.models.list() returns ≥ 11 entries with valid OpenAI shapes."""
    client = _client()
    models = list(client.models.list())
    assert len(models) >= 11, f"got {len(models)} models, want ≥ 11"
    ids = {m.id for m in models}
    missing = CANONICAL_MODEL_IDS - ids
    assert not missing, f"canonical IDs missing from /v1/models: {missing}"
    for m in models:
        assert m.object == "model", f"{m.id}: object={m.object!r}"
        assert m.created > 0, f"{m.id}: created={m.created}"
        assert m.owned_by in CANONICAL_OWNED_BY, (
            f"{m.id}: owned_by={m.owned_by!r} not in BR-1.5 closed-set"
        )


# Scenario: 3.5-E2E-002
# Priority: P1
@_skip_if_no_gateway
def test_models_list_unauthorized_raises_authentication_error():
    """Invalid API key → openai.AuthenticationError (401 → AuthenticationError mapping)."""
    bad = _client(api_key="invalid-key-not-issued")
    with pytest.raises(openai.AuthenticationError):
        list(bad.models.list())


# ---------------------------------------------------------------------------
# Story 4.7 — capability-field extension via the OpenAI SDK (Pydantic
# `extra="allow"` round-trip). The SDK pin is `openai==1.40.*` per Story 3.3
# cascade; Pydantic-v2 keeps extras accessible via `model_extra`. Do NOT
# bump the pin in Story 4.7 (Test Design note 3).
# ---------------------------------------------------------------------------

# BR-1.2 — the 7 capability dimensions in their canonical declaration order.
CAPABILITY_KEYS = (
    "chat",
    "streaming",
    "function_calling",
    "vision",
    "json_mode",
    "context_window_tokens",
    "max_output_tokens",
)


# Scenario: 4.7-CONTRACT-001
# Priority: P0
@_skip_if_no_gateway
def test_4_7_contract_001_v1_models_capabilities_field_via_openai_SDK():
    """`client.models.list()` exposes the He-API `capabilities` extension
    via Pydantic's `model_extra` accessor. Every entry has the 7-key
    sub-struct with the BR-1.2 types (bool × 5 + int × 2).
    """
    client = _client()
    models = list(client.models.list())
    assert len(models) >= 11

    for m in models:
        # Pydantic `extra="allow"` stows unknown fields under `model_extra`.
        extras = getattr(m, "model_extra", None) or {}
        caps = extras.get("capabilities")
        assert caps is not None, f"{m.id}: missing capabilities in model_extra"
        assert isinstance(caps, dict), f"{m.id}: capabilities is {type(caps)!r}"
        for k in CAPABILITY_KEYS:
            assert k in caps, f"{m.id}: capabilities missing key {k!r}"
        for k in ("chat", "streaming", "function_calling", "vision", "json_mode"):
            assert isinstance(caps[k], bool), (
                f"{m.id}: capabilities.{k} is {type(caps[k])!r}, want bool"
            )
        for k in ("context_window_tokens", "max_output_tokens"):
            assert isinstance(caps[k], int), (
                f"{m.id}: capabilities.{k} is {type(caps[k])!r}, want int"
            )

    # Spot-check a known entry — qwen-max chat=true per BR-1.3.
    qwen = next((m for m in models if m.id == "qwen-max"), None)
    assert qwen is not None, "qwen-max missing from /v1/models"
    qwen_caps = (getattr(qwen, "model_extra", None) or {}).get("capabilities") or {}
    assert qwen_caps.get("chat") is True
    assert qwen_caps.get("context_window_tokens") == 32768


# Scenario: 4.7-CONTRACT-002
# Priority: P0
@_skip_if_no_gateway
def test_4_7_contract_002_v1_models_pydantic_does_not_raise():
    """Pydantic `extra="allow"` MUST NOT raise on the capability-extended
    response. Wrap the call in a try/except so a future stricter Pydantic
    config surfaces a clear test failure rather than a CI-only stack trace.
    """
    import pydantic

    client = _client()
    try:
        models = list(client.models.list())
    except pydantic.ValidationError as exc:
        raise AssertionError(
            f"Pydantic validation failed on /v1/models — Story 3.3 SDK pin "
            f"(openai==1.40.*) or Pydantic config drifted: {exc}"
        ) from exc

    for m in models:
        assert isinstance(m, openai.types.Model), (
            f"{m!r} is {type(m).__name__}, want openai.types.Model"
        )
