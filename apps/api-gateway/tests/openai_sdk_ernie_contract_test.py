"""
Story 4.6 — Ernie (Baidu Qianfan v2 OpenAI-compat) Adapter (Streaming + Non-Streaming)
— OpenAI Python SDK contract tests.

AUTO-GENERATED skeleton by Dev (Story 4.6 implementation, 2026-05-19) per the
skeleton-only delivery posture inherited from Stories 4.1 + 4.2 + 4.3 + 4.4 + 4.5
— live execution deferred to a follow-up Story once a running-gateway CI lane is
provisioned.

Coverage matrix (per Story 4.6 T4.2 — 1 model × 2 paths = 2 contract scenarios):
  4.6-CONTRACT-001 — AC1 non-streaming happy path for `ernie-4.0`
  4.6-CONTRACT-002 — AC2 streaming happy path for `ernie-4.0` + tail-usage extraction

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set (gateway running, registry
  mapped for ernie-4.0, adapter-fake or real adapter pod reachable via
  ERNIE_ADAPTER_ENDPOINT). Live `qianfan.baidubce.com` exercise lives in
  openai_sdk_ernie_live_test.py (HE_API_ERNIE_LIVE=1 gated — separate from
  HE_API_DEEPSEEK_LIVE, HE_API_QWEN_LIVE, HE_API_KIMI_LIVE, HE_API_GLM_LIVE,
  HE_API_DOUBAO_LIVE so operators can run vendor suites independently).
"""

import os

import pytest
from openai import OpenAI

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.6 contract tests require a running "
           "gateway wired to the ernie-4.0 adapter (real or fake)",
)


ERNIE_MODEL = "ernie-4.0"


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1 — Non-streaming /v1/chat/completions via Ernie adapter
# ============================================================

# Scenario: 4.6-CONTRACT-001 — Priority P0
# AC1 + BR-1.10 + OQ-4.6-1 (Qianfan v2 OpenAI-compat endpoint) + N=1 dispatch
def test_chat_completions_ernie40_nonstream_happy_path():
    """SDK Pydantic-strict shape conformance for stream=False, model=ernie-4.0.
    Also asserts X-He-Cost-Usd header is ABSENT (REUSE Story-4.1 OQ5 binding).
    """
    # TODO: Implement per 4.6-CONTRACT-001
    # - client.chat.completions.create(model="ernie-4.0",
    #     messages=[{"role":"user","content":"Hello"}], stream=False)
    # - response.usage.prompt_tokens > 0
    # - response.model == "ernie-4.0"
    # - response.choices[0].message.role == "assistant"
    # - response.choices[0].finish_reason in {"stop","length","tool_calls","content_filter"}
    # - response headers contain "x-he-selected-model: ernie-4.0"
    # - response headers contain "x-he-request-id: req_<12-hex>"
    # - response headers DO NOT contain "x-he-cost-usd"
    pytest.fail("Not implemented: 4.6-CONTRACT-001")


# ============================================================
# AC2 — Streaming /v1/chat/completions via Ernie adapter
# ============================================================

# Scenario: 4.6-CONTRACT-002 — Priority P0
# AC2 + BR-2.9 (forced stream_options.include_usage=true) + OQ-4.6-4
def test_chat_completions_ernie40_streaming_happy_path():
    """SDK streaming iteration without openai.APIError; tail chunk carries usage."""
    # TODO: Implement per 4.6-CONTRACT-002
    # - for chunk in client.chat.completions.create(model="ernie-4.0",
    #     messages=[{"role":"user","content":"Tell me a haiku."}],
    #     stream=True, stream_options={"include_usage": True}): ...
    # - all chunks share same id
    # - chunk.object == "chat.completion.chunk"
    # - chunk.model == "ernie-4.0"
    # - last non-DONE iter's chunk.usage non-None with
    #   chunk.usage.total_tokens == chunk.usage.prompt_tokens + chunk.usage.completion_tokens
    pytest.fail("Not implemented: 4.6-CONTRACT-002")
