"""Story 3.5 — OpenAI Python SDK contract tests for POST /v1/embeddings.

Source: docs/qa/assessments/3.5-test-design-20260519.md

Skipped when env is unset (Story 3.3 BR-3.1 convention). CI runs them in
the gateway job after booting the api-gateway with auth-svc stubs and
minting a key via `scripts/issue-test-key.sh`.

Architect Round 1 OQ3 ruling: 128-dim mock vector (NOT 1536) — the dim is
intentionally non-canonical to signal "mock" at wire inspection.
"""

import math
import os

import httpx
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

MOCK_EMBEDDING_DIM = 128  # Architect Round 1 OQ3 — non-canonical, signals mock


def _client() -> openai.OpenAI:
    return openai.OpenAI(
        base_url=os.environ[GATEWAY_URL_ENV].rstrip("/") + "/v1",
        api_key=os.environ[API_KEY_ENV],
    )


# Scenario: 3.5-E2E-003
# Priority: P0
@_skip_if_no_gateway
def test_embeddings_create_single_input():
    """SDK Pydantic-parses /v1/embeddings cleanly; vector + usage shape valid."""
    client = _client()
    resp = client.embeddings.create(
        model="text-embedding-3-small",
        input="Hello, He-API.",
    )
    assert resp.object == "list"
    assert len(resp.data) == 1
    item = resp.data[0]
    assert item.object == "embedding"
    assert item.index == 0
    assert len(item.embedding) == MOCK_EMBEDDING_DIM, (
        f"len={len(item.embedding)}, want {MOCK_EMBEDDING_DIM} (Architect OQ3)"
    )
    norm = math.sqrt(sum(x * x for x in item.embedding))
    assert 0.99 <= norm <= 1.01, f"‖v‖₂={norm}, want ∈ [0.99, 1.01]"
    assert resp.model == "text-embedding-3-small"
    assert resp.usage.total_tokens == resp.usage.prompt_tokens, (
        "BR-2.5: usage.total_tokens MUST equal usage.prompt_tokens for embeddings"
    )


# Scenario: 3.5-E2E-004
# Priority: P1
@_skip_if_no_gateway
def test_embeddings_create_array_input():
    """Array input → one embedding entry per input, in order."""
    client = _client()
    resp = client.embeddings.create(
        model="text-embedding-3-small",
        input=["foo", "bar", "baz"],
    )
    assert len(resp.data) == 3
    for i, item in enumerate(resp.data):
        assert item.index == i, f"data[{i}].index = {item.index}"
        assert len(item.embedding) == MOCK_EMBEDDING_DIM


# Scenario: 3.5-E2E-005
# Priority: P0
@_skip_if_no_gateway
def test_embeddings_create_determinism_sdk_level():
    """BR-2.4 triple-layer guard (SDK tier): same input → byte-equal vectors."""
    client = _client()
    r1 = client.embeddings.create(model="text-embedding-3-small", input="Hello, He-API.")
    r2 = client.embeddings.create(model="text-embedding-3-small", input="Hello, He-API.")
    v1 = r1.data[0].embedding
    v2 = r2.data[0].embedding
    assert len(v1) == len(v2) == MOCK_EMBEDDING_DIM
    for i, (a, b) in enumerate(zip(v1, v2)):
        assert abs(a - b) < 1e-6, f"v1[{i}]={a} vs v2[{i}]={b}: differ > 1e-6"


# Scenario: 3.5-E2E-006
# Priority: P1
@_skip_if_no_gateway
def test_embeddings_missing_input_raises_bad_request():
    """Body without `input` field → 400 + error.param == "input".

    The OpenAI SDK validates `input` client-side, so we call via raw httpx
    to hit the gateway's wire-level envelope.
    """
    url = os.environ[GATEWAY_URL_ENV].rstrip("/") + "/v1/embeddings"
    headers = {
        "Authorization": f"Bearer {os.environ[API_KEY_ENV]}",
        "Content-Type": "application/json",
    }
    body = {"model": "text-embedding-3-small"}  # input missing
    r = httpx.post(url, headers=headers, json=body)
    assert r.status_code == 400, f"status={r.status_code}, body={r.text}"
    payload = r.json()
    err = payload.get("error") or {}
    assert err.get("code") == "400_invalid_request", err
    assert err.get("param") == "input", err
