"""
Story 4.2 — Qwen Adapter — LIVE upstream e2e tests (real dashscope.aliyuncs.com).

AUTO-GENERATED skeleton by QA test-design (2026-05-19). Dev (Story 4.2 implementation,
2026-05-19) ships skeleton-only delivery posture inherited from Story 4.1.

LIVE exercise gated by HE_API_QWEN_LIVE=1 (separate from HE_API_DEEPSEEK_LIVE so
operators can run vendor suites independently); CI defaults to skipping (these tests
consume DashScope vendor quota; the Qwen Max free-tier quota is tight and may flap
under load). Operator triggers via workflow_dispatch on demand.

Coverage matrix (per Story 4.2 T4.5):
  4.2-E2E-001 — qwen-max non-streaming English short (USAGE-001 fixture)
  4.2-E2E-002 — qwen-plus non-streaming English short
  4.2-E2E-003 — qwen-max streaming with include_usage tail-chunk
  4.2-E2E-004 — qwen-plus streaming
  4.2-E2E-005 — qwen-max token-usage oracle (replay ~10 fixtures × measure delta)
  4.2-E2E-006 — qwen-plus token-usage oracle

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guards:
  HE_API_TEST_GATEWAY_URL — must be set
  HE_API_QWEN_LIVE=1      — must be set to opt into live-upstream tests
  QWEN_UPSTREAM_API_KEY   — must be valid (Vault-managed per Architect Round 1 OQ3
    ratified path kv/data/he-api/upstream/qwen/; .env.local in dev per
    scripts/dev/seed-qwen-key.sh).

CRITICAL Qwen-specific caveat (Architect Round 1 BR-2.7): DashScope latency from
non-mainland-China CI runners (GitHub-hosted runners typically US-based) may
exceed the BR-2.7 500ms TTFB regression marker. Operators should either run the
live suite from a China-region runner (workflow_dispatch with self-hosted) OR
treat TTFB regression as advisory (capture p95 separately).
"""

import os
import time  # noqa: F401 — kept for future timing assertions

import pytest
from openai import OpenAI

pytestmark = [
    # Story 4.8 T4.13 / M-1 Path (ii) — see deepseek_live_test.py header.
    pytest.mark.skip(
        reason="folded into contract suite under Story 4.8 — pending deletion in housekeeping"
    ),
    pytest.mark.skipif(
        os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
        reason="HE_API_TEST_GATEWAY_URL not set",
    ),
    pytest.mark.skipif(
        os.environ.get("HE_API_QWEN_LIVE") != "1",
        reason="HE_API_QWEN_LIVE != 1 — live-upstream tests opted out (CI default)",
    ),
]


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1 live — Non-streaming against real dashscope.aliyuncs.com
# ============================================================

# Scenario: 4.2-E2E-001 — Priority P1
def test_live_qwen_max_nonstream_english_short():
    """AC1 live smoke for qwen-max (USAGE-001 fixture: English short)."""
    # TODO: Implement per 4.2-E2E-001
    # - response = client.chat.completions.create(model="qwen-max",
    #     messages=[{"role":"user","content":"Say hi."}], stream=False, temperature=0)
    # - response.usage populated; response.choices[0].finish_reason == "stop"
    pytest.fail("Not implemented: 4.2-E2E-001")


# Scenario: 4.2-E2E-002 — Priority P1
def test_live_qwen_plus_nonstream_english_short():
    """AC1 live smoke for qwen-plus."""
    # TODO: Implement per 4.2-E2E-002
    # - same as 4.2-E2E-001 with model="qwen-plus"
    pytest.fail("Not implemented: 4.2-E2E-002")


# ============================================================
# AC2 live — Streaming against real dashscope.aliyuncs.com
# ============================================================

# Scenario: 4.2-E2E-003 — Priority P1
def test_live_qwen_max_stream_with_include_usage():
    """AC2 live streaming for qwen-max; verify tail chunk carries usage."""
    # TODO: Implement per 4.2-E2E-003
    # - stream = client.chat.completions.create(model="qwen-max",
    #     messages=[{"role":"user","content":"Tell me a haiku."}],
    #     stream=True, stream_options={"include_usage": True}, temperature=0)
    # - chunks = list(stream); chunks[-1].usage.prompt_tokens > 0
    pytest.fail("Not implemented: 4.2-E2E-003")


# Scenario: 4.2-E2E-004 — Priority P1
def test_live_qwen_plus_stream_with_include_usage():
    """AC2 live streaming for qwen-plus."""
    # TODO: Implement per 4.2-E2E-004
    # - same as 4.2-E2E-003 with model="qwen-plus"
    pytest.fail("Not implemented: 4.2-E2E-004")


# ============================================================
# AC3 live — Token-usage oracle invariant (< 1% delta)
# ============================================================

# Scenario: 4.2-E2E-005 — Priority P1
def test_live_qwen_max_token_usage_oracle_under_one_percent_delta():
    """AC3 live oracle: replay ~10 fixtures × qwen-max; for each fixture, the
    adapter-reported usage matches upstream-reported usage within < 1% delta.
    """
    # TODO: Implement per 4.2-E2E-005
    # - Load ~10 fixtures from apps/adapters/qwen/tests/fixtures/usage/qwen-max/
    # - For each: call SDK, record reported usage, also separately call DashScope
    #   directly via raw HTTP (using QWEN_UPSTREAM_API_KEY) to capture the
    #   upstream-reported usage, assert abs(adapter - upstream)/upstream < 0.01.
    pytest.fail("Not implemented: 4.2-E2E-005")


# Scenario: 4.2-E2E-006 — Priority P1
def test_live_qwen_plus_token_usage_oracle_under_one_percent_delta():
    """AC3 live oracle for qwen-plus."""
    # TODO: Implement per 4.2-E2E-006
    # - same as 4.2-E2E-005 with model="qwen-plus" and qwen-plus fixtures
    pytest.fail("Not implemented: 4.2-E2E-006")
