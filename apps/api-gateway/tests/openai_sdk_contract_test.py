"""Story 3.3 AC3 — OpenAI Python SDK end-to-end contract test.

Reason: the SDK has had two breaking response-parsing changes in the last
12 months (BR-3.2 — pinned to ==1.40.*). The contract test confirms our
gateway's response shape parses cleanly under the SDK's Pydantic-based
validator — a class of bug no Go-side test can ever surface (a missing
field, a wrong JSON tag, or a string-vs-number type drift here would
fall through to customers as cryptic `openai.APIError` traces).

The tests SKIP when `HE_API_TEST_GATEWAY_URL` is unset (BR-3.1) so unit-
only test runs don't fail. CI sets it in the `gateway / openai-sdk-contract`
job (.github/workflows/test.yml) after booting the gateway with a Postgres
+ Redis testcontainers stack and minting a bearer key via
`scripts/issue-test-key.sh`.

Per BR-3.4 this file MUST NOT mock the OpenAI SDK — the entire value is
in confirming the real SDK accepts our shape. `test_sdk_not_mocked`
provides a defence against an accidental autouse pytest-mock fixture.
"""

# Scenario IDs trace docs/qa/assessments/3.3-test-design-20260518.md.
import os
import time
import unittest.mock

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


def _client() -> openai.OpenAI:
    """Construct a real OpenAI client pointed at the gateway under test."""
    return openai.OpenAI(
        base_url=os.environ[GATEWAY_URL_ENV].rstrip("/") + "/v1",
        api_key=os.environ[API_KEY_ENV],
    )


# Scenario: 3.3-E2E-001
@_skip_if_no_gateway
def test_chat_completions_non_streaming():
    """Happy path: SDK Pydantic-parses the gateway response cleanly."""
    client = _client()
    before = time.time()
    resp = client.chat.completions.create(
        model="qwen-max",
        messages=[{"role": "user", "content": "Say hi."}],
        stream=False,
    )
    after = time.time()

    # Shape assertions — every field the BR-3.x contract enumerates.
    assert resp.id.startswith("chatcmpl-mock-"), f"id={resp.id!r}"
    assert len(resp.id) == len("chatcmpl-mock-") + 12, f"id length={len(resp.id)}"
    assert resp.object == "chat.completion"
    assert resp.model == "qwen-max"
    assert len(resp.choices) == 1
    choice = resp.choices[0]
    assert choice.index == 0
    assert choice.message.role == "assistant"
    assert choice.message.content == (
        "Hello from He-API mock. Real upstream lands in Story 4.x."
    )
    assert choice.finish_reason == "stop"
    assert resp.usage.prompt_tokens == 10
    assert resp.usage.completion_tokens == 20
    assert resp.usage.total_tokens == 30
    # ±5 minute clock skew tolerance per AC3.
    assert before - 300 <= resp.created <= after + 300


# Scenario: 3.3-E2E-002
@_skip_if_no_gateway
def test_chat_completions_stream_rejected():
    """stream=True → APIStatusError(501) carrying the 501_streaming code."""
    client = _client()
    with pytest.raises(openai.APIStatusError) as excinfo:
        client.chat.completions.create(
            model="qwen-max",
            messages=[{"role": "user", "content": "Say hi."}],
            stream=True,
        )
    err = excinfo.value
    assert err.status_code == 501, f"status_code={err.status_code}"
    body = err.response.json()
    assert body["error"]["code"] == "501_streaming_not_implemented"
    assert body["error"]["type"] == "server_error"
    assert body["error"]["param"] == "stream"


# Scenario: 3.3-E2E-003
def test_sdk_pin_guard():
    """BR-3.2 defence against renegade `pip install -U openai`."""
    assert openai.__version__.startswith("1.40."), (
        f"openai SDK version drift: {openai.__version__!r} (expected 1.40.*)"
    )


# Scenario: 3.3-E2E-004
def test_sdk_not_mocked():
    """BR-3.4 — `openai.OpenAI` MUST be the real class, not a MagicMock."""
    assert not isinstance(openai.OpenAI, unittest.mock.MagicMock), (
        "openai.OpenAI is mocked — pytest-mock autouse fixture detected"
    )
