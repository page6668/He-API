"""Story 4.2 — Qwen Adapter — OpenAI Python SDK contract tests.

Story 4.8 T4.2 — skeleton bodies CONVERTED + rewired to the shared protocol
invariants library. Vendor delta layer: N=2 multi-model-id dispatch
(`qwen-max` + `qwen-plus`) per BR-1.10.

Coverage matrix (per Story 4.2 T4.4):
  4.2-CONTRACT-001 — AC1 non-streaming happy path for `qwen-max`
  4.2-CONTRACT-002 — AC1 non-streaming happy path for `qwen-plus`
  4.2-CONTRACT-003 — AC2 streaming happy path for `qwen-max`
  4.2-CONTRACT-004 — AC2 streaming happy path for `qwen-plus`
  4.2-CONTRACT-005 — AC2 streaming with include_usage tail-chunk assertion
  4.2-CONTRACT-006 — AC1 unknown model id → 400 + request-id propagation

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2)
"""

import os

import pytest
from openai import OpenAI

from _protocol_invariants import (
    REQUEST_ID_RE,
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
)

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.2 contract tests require a running "
           "gateway wired to qwen-max + qwen-plus adapters (real or fake)",
)


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


def _nonstream_happy(model: str):
    raw = _client().chat.completions.with_raw_response.create(
        model=model, messages=[{"role": "user", "content": "Hello"}], stream=False,
    )
    response = raw.parse()
    assert_chat_completion_shape(response, expected_model=model)
    selected = raw.headers.get("x-he-selected-model")
    assert selected == model, f"x-he-selected-model={selected!r}"
    rid = raw.headers.get("x-he-request-id")
    assert rid and REQUEST_ID_RE.match(rid), f"x-he-request-id={rid!r}"
    assert raw.headers.get("x-he-cost-usd") is None, "OQ5: x-he-cost-usd present"


def _stream_happy(model: str):
    chunks = list(_client().chat.completions.create(
        model=model, messages=[{"role": "user", "content": "Hi"}],
        stream=True, stream_options={"include_usage": True},
    ))
    assert len(chunks) >= 2
    first_id = chunks[0].id
    for c in chunks:
        assert c.id == first_id, "BR-1.4 id-continuity"
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model=model,
            is_bootstrap=(i == 0), is_terminal=(i == len(chunks) - 1),
        )


# ============================================================
# AC1 — Non-streaming
# ============================================================

# Scenario: 4.2-CONTRACT-001 — qwen-max non-streaming
def test_chat_completions_qwen_max_nonstream_happy_path():
    _nonstream_happy("qwen-max")


# Scenario: 4.2-CONTRACT-002 — qwen-plus non-streaming (BR-1.10 N=2 dispatch)
def test_chat_completions_qwen_plus_nonstream_happy_path():
    _nonstream_happy("qwen-plus")


# Scenario: 4.2-CONTRACT-006 — unknown model id
def test_chat_completions_qwen_unknown_model_raises_400_APIError():
    import openai

    with pytest.raises(openai.BadRequestError) as exc_info:
        _client().chat.completions.create(
            model="kimi-typo-not-wired", messages=[{"role": "user", "content": "Hi"}]
        )
    code = getattr(exc_info.value, "code", None)
    assert code == "400_invalid_request", f"exc.code={code!r}"
    rid = getattr(exc_info.value, "request_id", None) or ""
    assert REQUEST_ID_RE.match(rid), f"request_id={rid!r}"


# ============================================================
# AC2 — Streaming
# ============================================================

# Scenario: 4.2-CONTRACT-003 — qwen-max streaming
def test_chat_completions_qwen_max_stream_happy_path():
    _stream_happy("qwen-max")


# Scenario: 4.2-CONTRACT-004 — qwen-plus streaming
def test_chat_completions_qwen_plus_stream_happy_path():
    _stream_happy("qwen-plus")


# Scenario: 4.2-CONTRACT-005 — tail-usage invariant
def test_chat_completions_qwen_stream_tail_usage_chunk_present():
    chunks = list(_client().chat.completions.create(
        model="qwen-max", messages=[{"role": "user", "content": "Hi"}],
        stream=True, stream_options={"include_usage": True},
    ))
    tail = chunks[-1]
    assert tail.usage is not None, "BR-2.4 tail usage missing"
    u = tail.usage
    assert u.total_tokens == u.prompt_tokens + u.completion_tokens, (
        f"BR-3.3 additivity violation: {u.total_tokens=} {u.prompt_tokens=} {u.completion_tokens=}"
    )


# ============================================================
# Pin guard
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family"
    )
