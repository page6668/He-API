"""
Story 4.1 — DeepSeek Adapter — LIVE upstream e2e tests (real api.deepseek.com)

AUTO-GENERATED skeleton by QA test-design (2026-05-19). Dev MUST implement all `pytest.fail()`
blocks. Do NOT delete any test case — each maps to a designed scenario in:
  docs/qa/assessments/4.1-test-design-20260519.md

LIVE exercise gated by HE_API_DEEPSEEK_LIVE=1; CI defaults to skipping (these tests consume
vendor quota). Operator triggers via workflow_dispatch on demand.

SDK pin: openai==1.40.* (Story 3.3 BR-3.2 inheritance)
Env-var guards:
  HE_API_TEST_GATEWAY_URL — must be set
  HE_API_DEEPSEEK_LIVE=1  — must be set to opt into live-upstream tests
  DEEPSEEK_UPSTREAM_API_KEY — must be valid (Vault-managed per OQ3 in prod;
    .env.local in dev per scripts/dev/seed-deepseek-key.sh)
"""

import os
import time

import pytest
from openai import OpenAI

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
        reason="HE_API_TEST_GATEWAY_URL not set",
    ),
    pytest.mark.skipif(
        os.environ.get("HE_API_DEEPSEEK_LIVE") != "1",
        reason="HE_API_DEEPSEEK_LIVE != 1 — live-upstream tests opted out (CI default)",
    ),
]


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1 live — Non-streaming against real api.deepseek.com
# ============================================================

# Scenario: 4.1-E2E-001 — Priority P1
def test_live_deepseek_nonstream_english_short():
    """AC1 live-tier smoke (USAGE-001 fixture: English short)."""
    # TODO: Implement per 4.1-E2E-001
    # - response = client.chat.completions.create(model="deepseek-v3",
    #     messages=[{"role":"user","content":"Say hi."}], stream=False, temperature=0)
    # - response.usage populated; response.choices[0].finish_reason == "stop"
    pytest.fail("Not implemented: 4.1-E2E-001")


# Scenario: 4.1-E2E-002 — Priority P1
def test_live_deepseek_nonstream_chinese_long():
    """AC1 live Chinese coverage (USAGE-007 fixture: Chinese long).
    Chinese tokens typically 1.5-2× English byte ratio.
    """
    # TODO: Implement per 4.1-E2E-002
    # - response.usage.completion_tokens > 100
    pytest.fail("Not implemented: 4.1-E2E-002")


# Scenario: 4.1-E2E-003 — Priority P1
def test_live_deepseek_nonstream_max_tokens_bounded():
    """AC1 live max-tokens enforcement."""
    # TODO: Implement per 4.1-E2E-003
    # - request with max_tokens=10
    # - response.usage.completion_tokens <= 10
    # - response.choices[0].finish_reason == "length"
    pytest.fail("Not implemented: 4.1-E2E-003")


# ============================================================
# AC2 live — Streaming against real api.deepseek.com
# ============================================================

# Scenario: 4.1-E2E-004 — Priority P1
def test_live_deepseek_stream_english_short():
    """AC2 live smoke."""
    # TODO: Implement per 4.1-E2E-004
    # - stream = client.chat.completions.create(model="deepseek-v3", ..., stream=True,
    #     stream_options={"include_usage": True})
    # - chunks = list(stream); len(chunks) >= 2
    # - chunks[-1].usage is not None
    pytest.fail("Not implemented: 4.1-E2E-004")


# Scenario: 4.1-E2E-005 — Priority P1
def test_live_deepseek_stream_chinese_long_with_include_usage():
    """AC2 live Chinese coverage + BR-3.7 streaming-tail usage."""
    # TODO: Implement per 4.1-E2E-005
    # - chunks[-1].usage.prompt_tokens > 0
    # - chunks[-1].usage.completion_tokens > 100
    pytest.fail("Not implemented: 4.1-E2E-005")


# Scenario: 4.1-E2E-006 — Priority P1
# BR-2.7 TTFB regression marker — informational only, NOT a CI gate
def test_live_deepseek_stream_TTFB_under_500ms_p50():
    """Record first-byte arrival time across 10 invocations; assert p50 ≤ 500ms.
    Informational regression marker (BR-2.7) — flagged if upstream + adapter
    combined p50 regresses beyond Story 3.4 mock-baseline.
    """
    # TODO: Implement per 4.1-E2E-006
    # - For 10 invocations: record time to first yielded chunk
    # - p50 of the 10 samples ≤ 500ms
    # - On failure: log + flag for investigation, do NOT raise (informational)
    pytest.fail("Not implemented: 4.1-E2E-006")


# ============================================================
# AC3 live — Token-usage fixture replay (the load-bearing test)
# ============================================================

# Scenario: 4.1-E2E-007 — Priority P1
# AC3 + Epic 4 DoD line 2 LOAD-BEARING
def test_live_deepseek_token_usage_fixture_replay():
    """Drive the ~20 USAGE-001..020 fixtures against live api.deepseek.com;
    assert |adapter_usage - upstream_usage| / upstream_usage < 0.01 (BR-3.1 oracle).
    Identity-mapping makes this exact equality for DeepSeek.
    """
    # TODO: Implement per 4.1-E2E-007
    # - Load fixtures from apps/adapters/deepseek/tests/fixtures/usage/*.json
    # - For each fixture: run against live upstream, capture usage
    # - For each fixture: assert |adapter_usage - upstream_usage| / upstream_usage < 0.01
    pytest.fail("Not implemented: 4.1-E2E-007")


# ============================================================
# AC1 + AC2 live — Full SDK compatibility sweep
# ============================================================

# Scenario: 4.1-E2E-008 — Priority P1
def test_live_deepseek_OpenAI_SDK_full_compatibility_sweep():
    """For each fixture, invoke BOTH stream=False and stream=True via the SDK;
    assert NO raised openai.APIError; assert response shape conforms to Pydantic-strict.
    """
    # TODO: Implement per 4.1-E2E-008
    # - For each fixture: invoke both stream modes
    # - SDK call must not raise
    # - Response.usage.{prompt_tokens, completion_tokens, total_tokens} all > 0
    pytest.fail("Not implemented: 4.1-E2E-008")
