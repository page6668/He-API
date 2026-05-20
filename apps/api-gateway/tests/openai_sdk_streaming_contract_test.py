"""Story 3.4 — /v1/chat/completions Streaming (SSE) — OpenAI Python SDK contract tests.

Story 4.8 T4.7 — REWIRED to consume the shared protocol invariants library
per BR-2.10. Pre-existing mock-chunker semantics preserved (Story 3.4 mock
emits NO usage on terminal chunk; the MED-5 fold-in in
`_protocol_invariants.assert_chat_completion_chunk_shape` tolerates this
when include_usage is NOT set).

SDK pin: openai==1.40.* (Story 3.3 BR-3.2)
"""

import os

import pytest
from openai import OpenAI

from _protocol_invariants import assert_chat_completion_chunk_shape

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — streaming SDK contract tests require a running gateway (BR-3.1 + BR-3.4)",
)


MOCK_CONTENT = "Hello from He-API mock. Real upstream lands in Story 4.x."
MOCK_MODEL = "qwen-max"  # Story 3.4 mock-chunker uses qwen-max for the mock dispatch


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


def _collect_chunks():
    """Returns the full list of ChatCompletionChunk objects for a streaming request."""
    stream = _client().chat.completions.create(
        model=MOCK_MODEL,
        messages=[{"role": "user", "content": "Say hi."}],
        stream=True,
    )
    return list(stream)


# Scenario: 3.4-E2E-001 — Priority P0
def test_chat_completions_streaming_happy_path():
    chunks = _collect_chunks()
    assert len(chunks) >= 3, f"expected >=3 chunks; got {len(chunks)}"
    # MED-2 three-way disambiguation: chunk[0] is bootstrap; chunks[-1] is
    # terminal (mock chunker emits NO usage; helper tolerates per MED-5).
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model=MOCK_MODEL,
            is_bootstrap=(i == 0), is_terminal=(i == len(chunks) - 1),
        )


# Scenario: 3.4-E2E-002 — Priority P0
# BR-3.3: single source of truth — concat matches handlers.MockContent
def test_chat_completions_streaming_concat_equals_mock_content():
    chunks = _collect_chunks()
    concat = "".join(
        c.choices[0].delta.content
        for c in chunks
        if c.choices and c.choices[0].delta and c.choices[0].delta.content is not None
    )
    assert concat == MOCK_CONTENT, f"concat byte-mismatch\n got={concat!r}\nwant={MOCK_CONTENT!r}"


# Scenario: 3.4-E2E-003 — Priority P0
def test_chat_completions_streaming_finish_reason_stop():
    chunks = _collect_chunks()
    assert chunks[-1].choices[0].finish_reason == "stop"


# Scenario: 3.4-E2E-004 — Priority P1
def test_chat_completions_streaming_done_sentinel_consumed_cleanly():
    chunks = _collect_chunks()
    assert len(chunks) >= 1


# Scenario: 3.4-E2E-005 — Priority P1
def test_sdk_streaming_pin_guard():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family"
    )
