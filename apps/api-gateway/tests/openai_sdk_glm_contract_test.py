"""Story 4.4 — GLM (Zhipu) Adapter — OpenAI Python SDK contract tests.

Story 4.8 T4.4 — skeleton bodies CONVERTED + rewired to the shared protocol
invariants library. Vendor delta layer: N=1 degenerate dispatch (`glm-4` only).

Coverage matrix (per Story 4.4 T2.6 — 1 model × 2 paths):
  4.4-CONTRACT-001 — AC1 non-streaming happy path for `glm-4`
  4.4-CONTRACT-002 — AC2 streaming happy path for `glm-4` + tail-usage

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2)
"""

import os

import pytest
from openai import OpenAI

from _protocol_invariants import (
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
)

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.4 contract tests require a running "
           "gateway wired to the glm-4 adapter (real or fake)",
)

GLM_MODEL = "glm-4"


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


def test_chat_completions_glm4_nonstream_happy_path():
    response = _client().chat.completions.create(
        model=GLM_MODEL,
        messages=[{"role": "user", "content": "Hello"}],
        stream=False,
    )
    assert_chat_completion_shape(response, expected_model=GLM_MODEL)


def test_chat_completions_glm4_streaming_happy_path():
    chunks = list(_client().chat.completions.create(
        model=GLM_MODEL, messages=[{"role": "user", "content": "Tell me a haiku."}],
        stream=True, stream_options={"include_usage": True},
    ))
    assert len(chunks) >= 2
    first_id = chunks[0].id
    for c in chunks:
        assert c.id == first_id, "BR-1.4 id-continuity"
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model=GLM_MODEL,
            is_bootstrap=(i == 0), is_terminal=(i == len(chunks) - 1),
        )
    tail = chunks[-1]
    if tail.usage is not None:
        u = tail.usage
        assert u.total_tokens == u.prompt_tokens + u.completion_tokens
