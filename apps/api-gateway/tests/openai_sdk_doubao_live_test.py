"""
Story 4.5 — Doubao (Volcengine Ark v3) Adapter — LIVE upstream e2e tests (real ark.cn-beijing.volces.com).

AUTO-GENERATED skeleton by Dev (Story 4.5 implementation, 2026-05-19) following
the skeleton-only delivery posture inherited from Stories 4.1 + 4.2 + 4.3 + 4.4.

LIVE exercise gated by HE_API_DOUBAO_LIVE=1 (separate from HE_API_DEEPSEEK_LIVE,
HE_API_QWEN_LIVE, HE_API_KIMI_LIVE, and HE_API_GLM_LIVE so operators can run
vendor suites independently); CI defaults to skipping (these tests consume
Volcengine vendor quota). Operator triggers via workflow_dispatch on demand.

Coverage matrix (per Story 4.5 T4.2 — 2 models × 2 paths = 4 live scenarios):
  4.5-E2E-001 — doubao-pro non-streaming smoke + oracle invariant verification
  4.5-E2E-002 — doubao-pro streaming with include_usage tail-chunk + oracle delta < 1%
  4.5-E2E-003 — doubao-lite non-streaming smoke + oracle invariant
  4.5-E2E-004 — doubao-lite streaming + oracle delta < 1%

R10 anchor: EVERY scenario asserts `response.model == "doubao-pro"|"doubao-lite"`
(NEVER the live Volcengine endpoint id `ep-20240xxx-xxxx`) — verifies BR-1.11
inbound back-translate against the REAL upstream end-to-end.

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guards:
  HE_API_TEST_GATEWAY_URL — must be set
  HE_API_DOUBAO_LIVE=1    — must be set to opt into live-upstream tests
  DOUBAO_UPSTREAM_API_KEY — must be valid (Vault-managed per Story-4.1 OQ3
    cascade ratified path kv/data/he-api/upstream/doubao/; .env.local in dev
    per scripts/dev/seed-doubao-key.sh).
  DOUBAO_PRO_ENDPOINT_ID  — operator-provisioned Volcengine endpoint id for doubao-pro
  DOUBAO_LITE_ENDPOINT_ID — operator-provisioned Volcengine endpoint id for doubao-lite

CRITICAL Doubao-specific caveats (Architect Round 1 BR-2.7):
  - BR-2.7 TTFB regression budget (< 500ms) applies ONLY to the loopback
    (mocked-upstream) path; live exercises against ark.cn-beijing.volces.com
    from non-mainland-China CI runners typically exceed 500ms due to
    cross-region network latency. Operators should run from China-region
    self-hosted runners OR treat TTFB regression as advisory (capture p95
    separately).
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
        os.environ.get("HE_API_DOUBAO_LIVE") != "1",
        reason="HE_API_DOUBAO_LIVE != 1 — live-upstream tests opted out (CI default)",
    ),
]


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# Scenario: 4.5-E2E-001 — Priority P1 (LIVE)
def test_doubao_pro_live_nonstream_smoke():
    """Real ark.cn-beijing.volces.com, model=doubao-pro, stream=False, "Hello" → 200; usage non-zero."""
    # TODO: Implement per 4.5-E2E-001
    # - response = client.chat.completions.create(
    #       model="doubao-pro",
    #       messages=[{"role":"user","content":"Hello"}],
    #       stream=False,
    #   )
    # - assert response.usage.prompt_tokens > 0
    # - assert response.usage.completion_tokens >= 0
    # - assert response.usage.total_tokens == response.usage.prompt_tokens + response.usage.completion_tokens
    # - assert response.model == "doubao-pro"  # R10 — never the live endpoint id
    # - assert not response.model.startswith("ep-")  # leak-prevention guard
    pytest.fail("Not implemented: 4.5-E2E-001")


# Scenario: 4.5-E2E-002 — Priority P1 (LIVE)
def test_doubao_pro_live_streaming_tail_usage_oracle_delta():
    """Live streaming with include_usage; tail-chunk usage; oracle delta < 1%; per-chunk back-translate."""
    # TODO: Implement per 4.5-E2E-002
    # - for chunk in client.chat.completions.create(
    #       model="doubao-pro",
    #       messages=[{"role":"user","content":"Tell me a haiku."}],
    #       stream=True,
    #       stream_options={"include_usage": True},
    #   ): ...
    # - tail chunk's usage non-None
    # - EVERY chunk.model == "doubao-pro"  # R10 per-chunk back-translate
    # - for fixture in FIXTURES (~10 prompts):
    #     abs(adapter_usage - upstream_usage) / upstream_usage < 0.01
    pytest.fail("Not implemented: 4.5-E2E-002")


# Scenario: 4.5-E2E-003 — Priority P1 (LIVE)
def test_doubao_lite_live_nonstream_smoke():
    """Live non-streaming smoke for doubao-lite — distinct endpoint id from pro."""
    # TODO: Implement per 4.5-E2E-003 — mirrors -001 with "doubao-lite".
    # - assert response.model == "doubao-lite"
    pytest.fail("Not implemented: 4.5-E2E-003")


# Scenario: 4.5-E2E-004 — Priority P1 (LIVE)
def test_doubao_lite_live_streaming_oracle_delta():
    """Live streaming for doubao-lite — cross-model per-chunk back-translate independence end-to-end."""
    # TODO: Implement per 4.5-E2E-004 — mirrors -002 with "doubao-lite".
    # - EVERY chunk.model == "doubao-lite"
    pytest.fail("Not implemented: 4.5-E2E-004")
