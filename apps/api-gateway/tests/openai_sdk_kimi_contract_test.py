"""
Story 4.3 — Kimi (Moonshot) Adapter (Streaming + Non-Streaming) — OpenAI Python SDK contract tests.

AUTO-GENERATED skeleton by Dev (Story 4.3 implementation, 2026-05-19) per the
skeleton-only delivery posture inherited from Stories 4.1 + 4.2 — live execution
deferred to a follow-up Story once a running-gateway CI lane is provisioned.

Coverage matrix (per Story 4.3 T4.4 — 3 models × 2 paths):
  4.3-CONTRACT-001 — AC1 non-streaming happy path for `moonshot-v1-8k`
  4.3-CONTRACT-002 — AC1 non-streaming happy path for `moonshot-v1-32k`
  4.3-CONTRACT-003 — AC1 non-streaming happy path for `moonshot-v1-128k`
  4.3-CONTRACT-004 — AC2 streaming happy path for `moonshot-v1-8k`
  4.3-CONTRACT-005 — AC2 streaming happy path for `moonshot-v1-32k`
  4.3-CONTRACT-006 — AC2 streaming happy path for `moonshot-v1-128k`

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guard: HE_API_TEST_GATEWAY_URL must be set (gateway running, registry mapped
  for ALL THREE Kimi model ids, adapter-fake or real adapter pod reachable via
  KIMI_ADAPTER_ENDPOINT). Live `api.moonshot.cn` exercise lives in
  openai_sdk_kimi_live_test.py (HE_API_KIMI_LIVE=1 gated — separate from
  HE_API_DEEPSEEK_LIVE and HE_API_QWEN_LIVE so operators can run vendor suites
  independently).
"""

import os

import pytest
from openai import OpenAI

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


# ============================================================
# AC1 — Non-streaming /v1/chat/completions via Kimi adapter
# ============================================================

# Scenario: 4.3-CONTRACT-001 — Priority P0
# AC1 + BR-1.10 + OQ-4.3-1 (Moonshot OpenAI-compat endpoint) + M2 N=3 dispatch
def test_chat_completions_moonshot_v1_8k_nonstream_happy_path():
    """SDK Pydantic-strict shape conformance for stream=False, model=moonshot-v1-8k.
    Also asserts X-He-Cost-Usd header is ABSENT (REUSE Story-4.1 OQ5 binding).
    """
    # TODO: Implement per 4.3-CONTRACT-001
    # - client.chat.completions.create(model="moonshot-v1-8k",
    #     messages=[{"role":"user","content":"Hello"}], stream=False)
    # - response.usage.prompt_tokens > 0
    # - response.model == "moonshot-v1-8k"
    # - response.choices[0].message.role == "assistant"
    # - response.choices[0].finish_reason in {"stop","length","tool_calls","content_filter"}
    # - response headers contain "x-he-selected-model: moonshot-v1-8k"
    # - response headers contain "x-he-request-id: req_<12-hex>"
    # - response headers DO NOT contain "x-he-cost-usd"
    pytest.fail("Not implemented: 4.3-CONTRACT-001")


# Scenario: 4.3-CONTRACT-002 — Priority P0
# AC1 for moonshot-v1-32k (middle size variant)
def test_chat_completions_moonshot_v1_32k_nonstream_happy_path():
    """Same shape contract as CONTRACT-001 but for model=moonshot-v1-32k."""
    # TODO: Implement per 4.3-CONTRACT-002
    pytest.fail("Not implemented: 4.3-CONTRACT-002")


# Scenario: 4.3-CONTRACT-003 — Priority P0
# AC1 for moonshot-v1-128k (third size variant, exercises M2 endpoint-dedup N=3)
def test_chat_completions_moonshot_v1_128k_nonstream_happy_path():
    """Same shape contract as CONTRACT-001 but for model=moonshot-v1-128k —
    exercises the THREE-model-id-per-vendor topology end-to-end (M2 N=3
    Architect Round 1 OQ-4.3-5 ratification).
    """
    # TODO: Implement per 4.3-CONTRACT-003
    pytest.fail("Not implemented: 4.3-CONTRACT-003")


# ============================================================
# AC2 — Streaming /v1/chat/completions via Kimi adapter
# ============================================================

# Scenario: 4.3-CONTRACT-004 — Priority P0
def test_chat_completions_moonshot_v1_8k_stream_happy_path():
    """SDK streaming iterator completes without raising; chunk-shape invariants
    preserved for model=moonshot-v1-8k. Tail chunk carries `usage` populated
    per BR-2.4 + BR-2.9 (force include_usage=true).
    """
    # TODO: Implement per 4.3-CONTRACT-004
    # - stream = client.chat.completions.create(model="moonshot-v1-8k", ...,
    #     stream=True, stream_options={"include_usage": True})
    # - chunks = list(stream)
    # - len(chunks) >= 2
    # - all chunk.object == "chat.completion.chunk"
    # - all chunk.model == "moonshot-v1-8k"
    # - all chunks share the same id (Story 3.4 BR-1.4 id-continuity)
    # - chunks[-1].usage is not None
    # - chunks[-1].usage.total_tokens == prompt_tokens + completion_tokens
    pytest.fail("Not implemented: 4.3-CONTRACT-004")


# Scenario: 4.3-CONTRACT-005 — Priority P0
def test_chat_completions_moonshot_v1_32k_stream_happy_path():
    """Same streaming contract as CONTRACT-004 but for model=moonshot-v1-32k."""
    # TODO: Implement per 4.3-CONTRACT-005
    pytest.fail("Not implemented: 4.3-CONTRACT-005")


# Scenario: 4.3-CONTRACT-006 — Priority P0
def test_chat_completions_moonshot_v1_128k_stream_happy_path():
    """Same streaming contract as CONTRACT-004 but for model=moonshot-v1-128k —
    Kimi-flagship long-document variant.
    """
    # TODO: Implement per 4.3-CONTRACT-006
    pytest.fail("Not implemented: 4.3-CONTRACT-006")


# ============================================================
# Pin guard — keep parity with Stories 3.3 / 3.4 / 3.5 / 4.1 / 4.2.
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family — "
        "if intentional, update the inheriting Story pin guards together."
    )
