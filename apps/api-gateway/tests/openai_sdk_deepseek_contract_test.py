"""
Story 4.1 — DeepSeek Adapter (Streaming + Non-Streaming) — OpenAI Python SDK contract tests

AUTO-GENERATED skeleton by QA test-design (2026-05-19). Dev MUST implement all `pytest.fail()`
blocks. Do NOT delete any test case — each maps to a designed scenario in:
  docs/qa/assessments/4.1-test-design-20260519.md
If a scenario becomes non-applicable, change to `@pytest.mark.skip(reason="<why>")`.

SDK pin: openai==1.40.* (Story 3.3 BR-3.2 inheritance; Story 4.1 BR-3.2)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set (gateway running, registry mapped for
  model=deepseek-v3, adapter-fake or real adapter pod reachable via the registry endpoint).
  Upstream is the `httptest.NewTLSServer` fake — live `api.deepseek.com` exercise lives in
  openai_sdk_deepseek_live_test.py (HE_API_DEEPSEEK_LIVE=1 gated).
"""

import os

import pytest
from openai import OpenAI

pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — Story 4.1 contract tests require a running "
           "gateway wired to a deepseek-v3 adapter (real or fake)",
)


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1: Non-streaming /v1/chat/completions via DeepSeek adapter
# ============================================================

# Scenario: 4.1-CONTRACT-001 — Priority P0
# AC1 + BR-8.x + OQ5 anchor + R1 mitigation
def test_chat_completions_deepseek_nonstream_happy_path():
    """SDK Pydantic-strict shape conformance for stream=False.
    Also asserts X-He-Cost-Usd header is ABSENT (Architect Round 2 OQ5 binding —
    cost calculation lands in Epic 7 billing-svc, NOT Story 4.1).
    """
    # TODO: Implement per 4.1-CONTRACT-001
    # - client.chat.completions.create(model="deepseek-v3",
    #     messages=[{"role":"user","content":"Hello"}], stream=False)
    # - response.usage.prompt_tokens > 0
    # - response.model == "deepseek-v3"
    # - response.choices[0].message.role == "assistant"
    # - response.choices[0].finish_reason in {"stop","length","tool_calls","content_filter"}
    # - response headers contain "x-he-selected-model: deepseek-v3"
    # - response headers contain "x-he-request-id: req_<12-hex>"
    # - response headers DO NOT contain "x-he-cost-usd" (OQ5 binding)
    pytest.fail("Not implemented: 4.1-CONTRACT-001")


# Scenario: 4.1-CONTRACT-004 — Priority P0
# AC1 WHEN unknown model + BR-5.x
def test_chat_completions_deepseek_unknown_model_raises_400_APIError():
    """SDK-side surfaced error for an unresolvable model id."""
    # TODO: Implement per 4.1-CONTRACT-004
    # - client.chat.completions.create(model="unknown-typo", ...) raises openai.BadRequestError
    # - Error body parses as 5-field §5.1.2 envelope with code="400_invalid_request"
    pytest.fail("Not implemented: 4.1-CONTRACT-004")


# ============================================================
# AC2: Streaming /v1/chat/completions via DeepSeek adapter
# ============================================================

# Scenario: 4.1-CONTRACT-002 — Priority P0
# AC2 + BR-8.x
def test_chat_completions_deepseek_stream_happy_path():
    """SDK streaming iterator completes without raising; chunk-shape invariants preserved."""
    # TODO: Implement per 4.1-CONTRACT-002
    # - stream = client.chat.completions.create(model="deepseek-v3", ...,
    #     stream=True, stream_options={"include_usage": True})
    # - chunks = list(stream)
    # - len(chunks) >= 2
    # - all chunk.object == "chat.completion.chunk"
    # - all chunk.model == "deepseek-v3"
    # - all chunks share the same id (Story 3.4 BR-1.4 id-continuity inheritance)
    pytest.fail("Not implemented: 4.1-CONTRACT-002")


# Scenario: 4.1-CONTRACT-003 — Priority P0
# AC2 + BR-2.4 + AC3 streaming-tail invariant
def test_chat_completions_deepseek_stream_tail_usage_chunk_present():
    """LAST non-DONE chunk's chunk.usage is populated with three positive integers;
    total == prompt + completion exact equality.
    """
    # TODO: Implement per 4.1-CONTRACT-003
    # - Same setup as CONTRACT-002
    # - Find the last chunk that is NOT [DONE] (in SDK iterator: chunks[-1])
    # - assert chunks[-1].usage is not None
    # - assert chunks[-1].usage.prompt_tokens > 0
    # - assert chunks[-1].usage.completion_tokens > 0
    # - assert chunks[-1].usage.total_tokens > 0
    # - assert chunks[-1].usage.total_tokens == chunks[-1].usage.prompt_tokens + chunks[-1].usage.completion_tokens
    pytest.fail("Not implemented: 4.1-CONTRACT-003")


# Scenario: 4.1-CONTRACT-005 — Priority P0
# AC2 WHEN upstream 5xx after first chunk + BR-2.6 + R1 mitigation
def test_chat_completions_deepseek_stream_mid_upstream_interruption_emits_SSE_error_frame():
    """Fake upstream injects mid-stream RST (gateway-side mock fixture, NOT Toxiproxy —
    Toxiproxy is in the chaos suite). SDK iterator yields a chunk with chunk.error OR
    raises openai.APIError on the next iteration.
    """
    # TODO: Implement per 4.1-CONTRACT-005
    # - Configure fake upstream to RST after first chunk (mock fixture switch)
    # - Iterate the streaming response
    # - assert either:
    #     (a) one yielded chunk has a non-None error attribute with code == "502_upstream_unavailable", OR
    #     (b) the iterator raises openai.APIError with body code == "502_upstream_unavailable"
    pytest.fail("Not implemented: 4.1-CONTRACT-005")


# ============================================================
# BR-1.5 Request-ID Continuity Across gateway↔adapter hop
# ============================================================

# Scenario: 4.1-CONTRACT-006 — Priority P0
# BR-1.5 + R3 mitigation
def test_chat_completions_deepseek_request_id_continuity_gateway_to_adapter_hop():
    """SDK-observed end-to-end request-id continuity across four observation points:
      (a) gateway entry middleware (Story 3.6)
      (b) gateway → adapter Connect-RPC outbound
      (c) adapter → upstream HTTPS outbound
      (d) adapter slog event=adapter_chat_request_end record

    Equality across all four observation points.
    """
    # TODO: Implement per 4.1-CONTRACT-006
    # - response = client.chat.completions.create(model="deepseek-v3", ...)
    # - sdk_observed = response._raw_response.headers.get("x-he-request-id")
    # - Gather slog records from gateway + adapter (via log-collector or test-side log buffer)
    # - assert all four observation points carry the same req_<12-hex> value
    pytest.fail("Not implemented: 4.1-CONTRACT-006")


# ============================================================
# Pin guard — keep parity with Story 3.3 / 3.4 / 3.5 SDK pin
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family — "
        "if intentional, update Story 3.3 BR-3.2 + Story 4.1 BR-3.2 pin guards together."
    )
