"""
Story 4.5 — Doubao (Volcengine Ark v3) Adapter (Streaming + Non-Streaming) — OpenAI Python SDK contract tests.

AUTO-GENERATED skeleton by Dev (Story 4.5 implementation, 2026-05-19) per the
skeleton-only delivery posture inherited from Stories 4.1 + 4.2 + 4.3 + 4.4 —
live execution deferred to a follow-up Story once a running-gateway CI lane is
provisioned.

Coverage matrix (per Story 4.5 T2.6 — 2 models × 2 paths = 4 contract scenarios):
  4.5-CONTRACT-001 — AC1 non-streaming happy path for `doubao-pro`
  4.5-CONTRACT-002 — AC1 non-streaming happy path for `doubao-lite` (cross-model independence)
  4.5-CONTRACT-003 — AC2 streaming happy path for `doubao-pro` + tail-usage + per-chunk back-translate
  4.5-CONTRACT-004 — AC2 streaming happy path for `doubao-lite` (cross-model back-translate independence)

R10 anchor: EVERY scenario asserts `response.model == "doubao-pro"|"doubao-lite"`
(NEVER the Volcengine endpoint id `ep-...`) — BR-1.11 inbound back-translate
SDK-observed verification. SDK consumers expect `response.model == request.model`
per OpenAI's echo convention; surfacing the endpoint id would leak deployment-time
configuration to API consumers.

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set (gateway running, registry mapped
  for doubao-pro + doubao-lite, adapter-fake or real adapter pod reachable via
  DOUBAO_ADAPTER_ENDPOINT). Live `ark.cn-beijing.volces.com` exercise lives in
  openai_sdk_doubao_live_test.py (HE_API_DOUBAO_LIVE=1 gated — separate from
  HE_API_DEEPSEEK_LIVE / HE_API_QWEN_LIVE / HE_API_KIMI_LIVE / HE_API_GLM_LIVE
  so operators can run vendor suites independently).
"""

import os

import pytest
from openai import OpenAI

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.5 contract tests require a running "
           "gateway wired to the doubao adapter (real or fake)",
)


DOUBAO_PRO = "doubao-pro"
DOUBAO_LITE = "doubao-lite"


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1 — Non-streaming /v1/chat/completions via Doubao adapter
# ============================================================

# Scenario: 4.5-CONTRACT-001 — Priority P0
# AC1 + BR-1.11 (R10 leak-prevention) + OQ-4.5-1 (Volcengine Ark v3) + N=2 dispatch
def test_doubao_pro_nonstreaming():
    """SDK Pydantic-strict shape conformance for stream=False, model=doubao-pro.

    R10 LEAK-PREVENTION assertion: response.model MUST equal "doubao-pro"
    (NEVER the Volcengine endpoint id `ep-...`) — verifies BR-1.11 inbound
    back-translate via the SDK lens (catches Pydantic-strict shape errors
    that pure JSON-shape assertions miss).

    Also asserts X-He-Cost-Usd header is ABSENT (REUSE Story-4.1 OQ5 binding).
    """
    # TODO: Implement per 4.5-CONTRACT-001
    # - client.chat.completions.create(model="doubao-pro",
    #     messages=[{"role":"user","content":"Hello"}], stream=False)
    # - response.usage.prompt_tokens > 0
    # - response.model == "doubao-pro"  # R10 leak-prevention
    # - NOT response.model.startswith("ep-")  # endpoint-id leak guard
    # - response.choices[0].message.role == "assistant"
    # - response.choices[0].finish_reason in {"stop","length","tool_calls","content_filter"}
    # - response headers contain "x-he-selected-model: doubao-pro"
    # - response headers contain "x-he-request-id: req_<12-hex>"
    # - response headers DO NOT contain "x-he-cost-usd"
    pytest.fail("Not implemented: 4.5-CONTRACT-001")


# Scenario: 4.5-CONTRACT-002 — Priority P0
# AC1 + BR-1.11 (R10) + R8 cross-model independence
def test_doubao_lite_nonstreaming():
    """SDK non-streaming for model=doubao-lite — cross-model back-translate independence."""
    # TODO: Implement per 4.5-CONTRACT-002 — mirrors -001 with "doubao-lite".
    # - response.model == "doubao-lite"  # R10 + R8 (cross-model)
    # - x-he-selected-model: doubao-lite
    pytest.fail("Not implemented: 4.5-CONTRACT-002")


# ============================================================
# AC2 — Streaming /v1/chat/completions via Doubao adapter
# ============================================================

# Scenario: 4.5-CONTRACT-003 — Priority P0
# AC2 + AC3 + BR-1.11 (per-chunk back-translate) + BR-2.9 (forced include_usage) + OQ-4.5-5
def test_doubao_pro_streaming():
    """SDK streaming iteration without openai.APIError; tail chunk carries usage;
    EVERY chunk.model == "doubao-pro" (NEVER endpoint id) per BR-1.11 per-chunk back-translate."""
    # TODO: Implement per 4.5-CONTRACT-003
    # - chunks = list(client.chat.completions.create(model="doubao-pro",
    #     messages=[{"role":"user","content":"Tell me a haiku."}],
    #     stream=True, stream_options={"include_usage": True}))
    # - all chunks share same id
    # - all chunks have chunk.object == "chat.completion.chunk"
    # - EVERY chunk.model == "doubao-pro" (R10 leak-prevention; NEVER startswith("ep-"))
    # - last non-DONE iter's chunk.usage non-None with
    #   chunk.usage.total_tokens == chunk.usage.prompt_tokens + chunk.usage.completion_tokens
    pytest.fail("Not implemented: 4.5-CONTRACT-003")


# Scenario: 4.5-CONTRACT-004 — Priority P0
# AC2 + AC3 + BR-1.11 + R10 + R8 cross-model streaming back-translate
def test_doubao_lite_streaming():
    """SDK streaming for model=doubao-lite — cross-model per-chunk back-translate independence.

    Even though pro and lite share ONE underlying ClientHandle (M2 dedup N=2
    RESTORATION per 4.5-UNIT-013), each request's per-chunk back-translate
    MUST use that request's req.Model (closure pattern, NOT a ReverseLookup —
    per Architect Round 1 l-1 ratification)."""
    # TODO: Implement per 4.5-CONTRACT-004 — mirrors -003 with "doubao-lite".
    # - EVERY chunk.model == "doubao-lite"  # NEVER "doubao-pro" (cross-model leak guard)
    pytest.fail("Not implemented: 4.5-CONTRACT-004")
