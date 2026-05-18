"""
Story 3.4 — /v1/chat/completions Streaming (SSE) — OpenAI Python SDK contract tests

Verifies the real customer iterator path against the gateway-under-test.

Test Design: docs/qa/assessments/3.4-test-design-20260518.md
SDK pin: openai==1.40.* (Story 3.3 BR-3.2; reused per Story 3.4 BR-3.2)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set; if absent, all tests
skip per BR-3.1 + BR-3.4.
"""

import os

import pytest
from openai import OpenAI

# Skip-if guard per BR-3.1 — never silently pass when the gateway is unavailable.
pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — streaming SDK contract tests require a running gateway (BR-3.1 + BR-3.4)",
)


MOCK_CONTENT = "Hello from He-API mock. Real upstream lands in Story 4.x."


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


def _collect_chunks():
    """Returns the full list of ChatCompletionChunk objects for a streaming request."""
    stream = _client().chat.completions.create(
        model="qwen-max",
        messages=[{"role": "user", "content": "Say hi."}],
        stream=True,
    )
    return list(stream)


# Scenario: 3.4-E2E-001 — Priority P0
def test_chat_completions_streaming_happy_path():
    chunks = _collect_chunks()
    assert len(chunks) >= 3, f"expected >=3 chunks; got {len(chunks)}"
    # Bootstrap chunk: delta.role == "assistant"; delta.content is None.
    assert chunks[0].choices[0].delta.role == "assistant"
    assert chunks[0].choices[0].delta.content is None
    # Terminal chunk: finish_reason == "stop"; delta.content is None.
    assert chunks[-1].choices[0].finish_reason == "stop"
    assert chunks[-1].choices[0].delta.content is None
    # All chunks share id / created / model / object.
    first_id = chunks[0].id
    first_created = chunks[0].created
    first_model = chunks[0].model
    for c in chunks:
        assert c.id == first_id, "id MUST be threaded across all chunks (BR-1.4)"
        assert c.created == first_created, "created MUST be threaded across all chunks (BR-1.5)"
        assert c.model == first_model, "model MUST be threaded across all chunks (BR-1.6)"
        assert c.object == "chat.completion.chunk", (
            f"object MUST be chat.completion.chunk (BR-1.7); got {c.object}"
        )
    assert first_model == "qwen-max", "model echo per BR-1.6"


# Scenario: 3.4-E2E-002 — Priority P0
# BR-3.3: single source of truth — concat matches handlers.MockContent
def test_chat_completions_streaming_concat_equals_mock_content():
    chunks = _collect_chunks()
    concat = "".join(
        c.choices[0].delta.content
        for c in chunks
        if c.choices[0].delta.content is not None
    )
    assert concat == MOCK_CONTENT, f"concat byte-mismatch\n got={concat!r}\nwant={MOCK_CONTENT!r}"


# Scenario: 3.4-E2E-003 — Priority P0
def test_chat_completions_streaming_finish_reason_stop():
    chunks = _collect_chunks()
    assert chunks[-1].choices[0].finish_reason == "stop"


# Scenario: 3.4-E2E-004 — Priority P1
# BR-4.5 + AR1-R3: the SDK absorbs [DONE] and terminates the iterator cleanly.
def test_chat_completions_streaming_done_sentinel_consumed_cleanly():
    # list() completing without raising IS the assertion.
    chunks = _collect_chunks()
    assert len(chunks) >= 1


# Scenario: 3.4-E2E-005 — Priority P1
# BR-3.2: SDK pin guard — openai==1.40.*
def test_sdk_streaming_pin_guard():
    import openai

    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family — "
        "if intentional, update Story 3.3 BR-3.2 + Story 3.4 BR-3.2 pin guards together."
    )
