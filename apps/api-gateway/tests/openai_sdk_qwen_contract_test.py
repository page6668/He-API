"""
Story 4.2 — Qwen Adapter (Streaming + Non-Streaming) — OpenAI Python SDK contract tests.

AUTO-GENERATED skeleton by QA test-design (2026-05-19). Dev (Story 4.2 implementation,
2026-05-19) ships the skeleton-only delivery posture inherited from Story 4.1 — live
execution deferred to a follow-up Story once a running-gateway CI lane is provisioned.

Coverage matrix (per Story 4.2 T4.4):
  4.2-CONTRACT-001 — AC1 non-streaming happy path for `qwen-max`
  4.2-CONTRACT-002 — AC1 non-streaming happy path for `qwen-plus`
  4.2-CONTRACT-003 — AC2 streaming happy path for `qwen-max`
  4.2-CONTRACT-004 — AC2 streaming happy path for `qwen-plus`
  4.2-CONTRACT-005 — AC2 streaming with include_usage=true tail-chunk assertion
  4.2-CONTRACT-006 — AC1 unknown model id → 400 + request-id propagation continuity

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set (gateway running, registry mapped
  for model=qwen-max AND model=qwen-plus, adapter-fake or real adapter pod reachable
  via QWEN_ADAPTER_ENDPOINT). Live `dashscope.aliyuncs.com` exercise lives in
  openai_sdk_qwen_live_test.py (HE_API_QWEN_LIVE=1 gated — separate from
  HE_API_DEEPSEEK_LIVE).
"""

import os

import pytest
from openai import OpenAI

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


# ============================================================
# AC1 — Non-streaming /v1/chat/completions via Qwen adapter
# ============================================================

# Scenario: 4.2-CONTRACT-001 — Priority P0
# AC1 + BR-1.10 + OQ-4.2-1 (compat-mode endpoint) + R8 (multi-model dispatch)
def test_chat_completions_qwen_max_nonstream_happy_path():
    """SDK Pydantic-strict shape conformance for stream=False, model=qwen-max.
    Also asserts X-He-Cost-Usd header is ABSENT (REUSE Story-4.1 OQ5 binding).
    """
    # TODO: Implement per 4.2-CONTRACT-001
    # - client.chat.completions.create(model="qwen-max",
    #     messages=[{"role":"user","content":"Hello"}], stream=False)
    # - response.usage.prompt_tokens > 0
    # - response.model == "qwen-max"
    # - response.choices[0].message.role == "assistant"
    # - response.choices[0].finish_reason in {"stop","length","tool_calls","content_filter"}
    # - response headers contain "x-he-selected-model: qwen-max"
    # - response headers contain "x-he-request-id: req_<12-hex>"
    # - response headers DO NOT contain "x-he-cost-usd"
    pytest.fail("Not implemented: 4.2-CONTRACT-001")


# Scenario: 4.2-CONTRACT-002 — Priority P0
# AC1 + BR-1.10 multi-model dispatch (second model id)
def test_chat_completions_qwen_plus_nonstream_happy_path():
    """Same shape contract as CONTRACT-001 but for model=qwen-plus, exercising
    BR-1.10 multi-model-id-per-service dispatch (Architect Round 1 OQ-4.2-2
    ratification: single `adapter-qwen` service hosts BOTH `qwen-max` and
    `qwen-plus`).
    """
    # TODO: Implement per 4.2-CONTRACT-002
    # - client.chat.completions.create(model="qwen-plus", ..., stream=False)
    # - response.model == "qwen-plus"
    # - response headers contain "x-he-selected-model: qwen-plus"
    # - same Pydantic-strict shape invariants as CONTRACT-001
    pytest.fail("Not implemented: 4.2-CONTRACT-002")


# Scenario: 4.2-CONTRACT-006 — Priority P0
# AC1 WHEN unknown model + BR-1.5 request-id propagation
def test_chat_completions_qwen_unknown_model_raises_400_APIError():
    """SDK-side surfaced error for an unresolvable model id; adapter is NOT invoked.
    Also asserts X-He-Request-Id continuity through the 400-envelope response.
    """
    # TODO: Implement per 4.2-CONTRACT-006
    # - client.chat.completions.create(model="kimi-typo-not-wired", ...) raises openai.BadRequestError
    # - Error body parses as 5-field §5.1.2 envelope with code="400_invalid_request"
    # - response header carries x-he-request-id
    pytest.fail("Not implemented: 4.2-CONTRACT-006")


# ============================================================
# AC2 — Streaming /v1/chat/completions via Qwen adapter
# ============================================================

# Scenario: 4.2-CONTRACT-003 — Priority P0
# AC2 + BR-1.10 streaming + AC2 chunk-shape invariants
def test_chat_completions_qwen_max_stream_happy_path():
    """SDK streaming iterator completes without raising; chunk-shape invariants
    preserved for model=qwen-max.
    """
    # TODO: Implement per 4.2-CONTRACT-003
    # - stream = client.chat.completions.create(model="qwen-max", ...,
    #     stream=True, stream_options={"include_usage": True})
    # - chunks = list(stream)
    # - len(chunks) >= 2
    # - all chunk.object == "chat.completion.chunk"
    # - all chunk.model == "qwen-max"
    # - all chunks share the same id (Story 3.4 BR-1.4 id-continuity)
    pytest.fail("Not implemented: 4.2-CONTRACT-003")


# Scenario: 4.2-CONTRACT-004 — Priority P0
# AC2 + BR-1.10 streaming for qwen-plus
def test_chat_completions_qwen_plus_stream_happy_path():
    """Same streaming contract as CONTRACT-003 but for model=qwen-plus."""
    # TODO: Implement per 4.2-CONTRACT-004
    # - same as 003 with model="qwen-plus"
    pytest.fail("Not implemented: 4.2-CONTRACT-004")


# Scenario: 4.2-CONTRACT-005 — Priority P0
# AC2 + BR-2.4 + AC3 streaming-tail invariant
def test_chat_completions_qwen_stream_tail_usage_chunk_present():
    """LAST non-DONE chunk's chunk.usage is populated with three positive
    integers; total == prompt + completion exact equality (BR-3.3 invariant).
    """
    # TODO: Implement per 4.2-CONTRACT-005
    # - Same setup as CONTRACT-003 (model=qwen-max)
    # - assert chunks[-1].usage is not None
    # - assert chunks[-1].usage.prompt_tokens > 0
    # - assert chunks[-1].usage.completion_tokens >= 0
    # - assert chunks[-1].usage.total_tokens == chunks[-1].usage.prompt_tokens + chunks[-1].usage.completion_tokens
    pytest.fail("Not implemented: 4.2-CONTRACT-005")


# ============================================================
# Pin guard — keep parity with Stories 3.3 / 3.4 / 3.5 / 4.1.
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family — "
        "if intentional, update the inheriting Story pin guards together."
    )
