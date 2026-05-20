"""Story 4.3 — Kimi (Moonshot) Adapter — OpenAI Python SDK contract tests.

Story 4.8 T4.3 — skeleton bodies CONVERTED + rewired to the shared protocol
invariants library. Vendor delta layer: N=3 multi-model-id dispatch
(`moonshot-v1-{8k,32k,128k}`) per BR-1.10. Optional BR-4.5 body-aware
context-length-exceeded path gated by the fake-upstream's TRIGGER_*
content marker (see apps/adapters/kimi/cmd/fake-upstream/main.go).

Coverage matrix:
  4.3-CONTRACT-001..003 — AC1 non-streaming for the three model variants
  4.3-CONTRACT-004..006 — AC2 streaming for the three model variants

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
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.3 contract tests require a running "
           "gateway wired to all three moonshot-v1-* adapters (real or fake)",
)

KIMI_MODELS = ("moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k")


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


def _nonstream_happy(model: str):
    response = _client().chat.completions.create(
        model=model, messages=[{"role": "user", "content": "Hello"}], stream=False,
    )
    assert_chat_completion_shape(response, expected_model=model)


def _stream_happy(model: str):
    chunks = list(_client().chat.completions.create(
        model=model, messages=[{"role": "user", "content": "Hi"}],
        stream=True, stream_options={"include_usage": True},
    ))
    assert len(chunks) >= 2
    first_id = chunks[0].id
    for c in chunks:
        assert c.id == first_id
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model=model,
            is_bootstrap=(i == 0), is_terminal=(i == len(chunks) - 1),
        )
    tail = chunks[-1]
    if tail.usage is not None:
        u = tail.usage
        assert u.total_tokens == u.prompt_tokens + u.completion_tokens


# ============================================================
# AC1 — Non-streaming (4.3-CONTRACT-001..003)
# ============================================================


def test_chat_completions_moonshot_v1_8k_nonstream_happy_path():
    _nonstream_happy("moonshot-v1-8k")


def test_chat_completions_moonshot_v1_32k_nonstream_happy_path():
    _nonstream_happy("moonshot-v1-32k")


def test_chat_completions_moonshot_v1_128k_nonstream_happy_path():
    _nonstream_happy("moonshot-v1-128k")


# ============================================================
# AC2 — Streaming (4.3-CONTRACT-004..006)
# ============================================================


def test_chat_completions_moonshot_v1_8k_stream_happy_path():
    _stream_happy("moonshot-v1-8k")


def test_chat_completions_moonshot_v1_32k_stream_happy_path():
    _stream_happy("moonshot-v1-32k")


def test_chat_completions_moonshot_v1_128k_stream_happy_path():
    _stream_happy("moonshot-v1-128k")


# ============================================================
# Pin guard
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40.")
