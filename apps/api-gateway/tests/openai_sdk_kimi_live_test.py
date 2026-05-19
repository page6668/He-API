"""
Story 4.3 — Kimi (Moonshot) Adapter — LIVE upstream e2e tests (real api.moonshot.cn).

AUTO-GENERATED skeleton by Dev (Story 4.3 implementation, 2026-05-19) following the
skeleton-only delivery posture inherited from Stories 4.1 + 4.2.

LIVE exercise gated by HE_API_KIMI_LIVE=1 (separate from HE_API_DEEPSEEK_LIVE and
HE_API_QWEN_LIVE so operators can run vendor suites independently); CI defaults to
skipping (these tests consume Moonshot vendor quota). Operator triggers via
workflow_dispatch on demand.

Coverage matrix (per Story 4.3 T4.5 — 3 sizes × 2 paths):
  4.3-E2E-001 — moonshot-v1-8k non-streaming English short (USAGE-001 fixture)
  4.3-E2E-002 — moonshot-v1-32k non-streaming English short
  4.3-E2E-003 — moonshot-v1-128k non-streaming long-document QA
  4.3-E2E-004 — moonshot-v1-8k streaming with include_usage tail-chunk
  4.3-E2E-005 — moonshot-v1-32k streaming
  4.3-E2E-006 — moonshot-v1-128k streaming (long-document workload)

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guards:
  HE_API_TEST_GATEWAY_URL — must be set
  HE_API_KIMI_LIVE=1      — must be set to opt into live-upstream tests
  KIMI_UPSTREAM_API_KEY   — must be valid (Vault-managed per Story-4.1 OQ3 cascade
    ratified path kv/data/he-api/upstream/kimi/; .env.local in dev per
    scripts/dev/seed-kimi-key.sh).

CRITICAL Kimi-specific caveats (Architect Round 1 BR-2.7 + m-2):
  - BR-2.7 TTFB regression budget (< 500ms) applies ONLY to the 8k variant on the
    loopback path; 32k/128k variants are NOT held to this budget because Moonshot
    upstream-side processing latency scales with context-window.
  - Moonshot endpoint reachability from non-mainland-China CI runners is variable
    (GitHub-hosted runners typically US-based); the 128k variant compounds
    cross-region latency. Operators should run from China-region self-hosted
    runners OR treat TTFB regression as advisory (capture p95 separately per m-2).
"""

import os
import time  # noqa: F401 — kept for future timing assertions

import pytest
from openai import OpenAI

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
        reason="HE_API_TEST_GATEWAY_URL not set",
    ),
    pytest.mark.skipif(
        os.environ.get("HE_API_KIMI_LIVE") != "1",
        reason="HE_API_KIMI_LIVE != 1 — live-upstream tests opted out (CI default)",
    ),
]


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# ============================================================
# AC1 live — Non-streaming against real api.moonshot.cn
# ============================================================

# Scenario: 4.3-E2E-001 — Priority P1
def test_live_moonshot_v1_8k_nonstream_english_short():
    """AC1 live smoke for moonshot-v1-8k (USAGE-001 fixture: English short)."""
    # TODO: Implement per 4.3-E2E-001
    # - response = client.chat.completions.create(model="moonshot-v1-8k",
    #     messages=[{"role":"user","content":"Say hi."}], stream=False, temperature=0)
    # - response.usage populated; response.choices[0].finish_reason == "stop"
    pytest.fail("Not implemented: 4.3-E2E-001")


# Scenario: 4.3-E2E-002 — Priority P1
def test_live_moonshot_v1_32k_nonstream_english_short():
    """AC1 live smoke for moonshot-v1-32k."""
    pytest.fail("Not implemented: 4.3-E2E-002")


# Scenario: 4.3-E2E-003 — Priority P1
def test_live_moonshot_v1_128k_nonstream_long_doc_qa():
    """AC1 live smoke for moonshot-v1-128k — flagship long-document QA workload.
    Fixture-driven: prompt ~50K tokens; expected completion ~200 tokens; usage
    delta < 1% per Epic 4 DoD line 2.
    """
    pytest.fail("Not implemented: 4.3-E2E-003")


# ============================================================
# AC2 live — Streaming against real api.moonshot.cn
# ============================================================

# Scenario: 4.3-E2E-004 — Priority P1
def test_live_moonshot_v1_8k_stream_with_include_usage():
    """AC2 live streaming for moonshot-v1-8k; verify tail chunk carries usage.
    BR-2.7 TTFB < 500ms regression marker scoped to this 8k variant only.
    """
    pytest.fail("Not implemented: 4.3-E2E-004")


# Scenario: 4.3-E2E-005 — Priority P1
def test_live_moonshot_v1_32k_stream_with_include_usage():
    """AC2 live streaming for moonshot-v1-32k. TTFB NOT asserted (BR-2.7 scoped
    to 8k only per m-2 affirmation).
    """
    pytest.fail("Not implemented: 4.3-E2E-005")


# Scenario: 4.3-E2E-006 — Priority P1
def test_live_moonshot_v1_128k_stream_with_include_usage():
    """AC2 live streaming for moonshot-v1-128k (long-document workload)."""
    pytest.fail("Not implemented: 4.3-E2E-006")
