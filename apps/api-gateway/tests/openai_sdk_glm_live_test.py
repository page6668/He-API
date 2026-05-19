"""
Story 4.4 — GLM (Zhipu) Adapter — LIVE upstream e2e tests (real open.bigmodel.cn).

AUTO-GENERATED skeleton by Dev (Story 4.4 implementation, 2026-05-19) following
the skeleton-only delivery posture inherited from Stories 4.1 + 4.2 + 4.3.

LIVE exercise gated by HE_API_GLM_LIVE=1 (separate from HE_API_DEEPSEEK_LIVE,
HE_API_QWEN_LIVE, and HE_API_KIMI_LIVE so operators can run vendor suites
independently); CI defaults to skipping (these tests consume Zhipu vendor quota).
Operator triggers via workflow_dispatch on demand.

Coverage matrix (per Story 4.4 T4.2 — 1 model × 2 paths):
  4.4-E2E-001 — glm-4 non-streaming smoke + oracle invariant verification
  4.4-E2E-002 — glm-4 streaming with include_usage tail-chunk + oracle delta < 1%

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2 pin)
Env-var guards:
  HE_API_TEST_GATEWAY_URL — must be set
  HE_API_GLM_LIVE=1       — must be set to opt into live-upstream tests
  GLM_UPSTREAM_API_KEY    — must be valid (Vault-managed per Story-4.1 OQ3
    cascade ratified path kv/data/he-api/upstream/glm/; .env.local in dev per
    scripts/dev/seed-glm-key.sh).

CRITICAL GLM-specific caveats (Architect Round 1 BR-2.7):
  - BR-2.7 TTFB regression budget (< 500ms) applies ONLY to the loopback
    (mocked-upstream) path; live exercises against open.bigmodel.cn from
    non-mainland-China CI runners typically exceed 500ms due to cross-region
    network latency. Operators should run from China-region self-hosted
    runners OR treat TTFB regression as advisory (capture p95 separately).
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
        os.environ.get("HE_API_GLM_LIVE") != "1",
        reason="HE_API_GLM_LIVE != 1 — live-upstream tests opted out (CI default)",
    ),
]


def _client() -> OpenAI:
    return OpenAI(
        base_url=os.environ["HE_API_TEST_GATEWAY_URL"].rstrip("/") + "/v1",
        api_key=os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub"),
    )


# Scenario: 4.4-E2E-001 — Priority P1 (LIVE)
def test_glm4_live_nonstream_smoke():
    """Real open.bigmodel.cn, model=glm-4, stream=False, "Hello" → 200; usage non-zero."""
    # TODO: Implement per 4.4-E2E-001
    # - response = client.chat.completions.create(
    #       model="glm-4",
    #       messages=[{"role":"user","content":"Hello"}],
    #       stream=False,
    #   )
    # - assert response.usage.prompt_tokens > 0
    # - assert response.usage.completion_tokens >= 0
    # - assert response.usage.total_tokens == response.usage.prompt_tokens + response.usage.completion_tokens
    pytest.fail("Not implemented: 4.4-E2E-001")


# Scenario: 4.4-E2E-002 — Priority P1 (LIVE)
def test_glm4_live_streaming_tail_usage_oracle_delta():
    """Live streaming with include_usage; tail-chunk usage; oracle delta < 1%."""
    # TODO: Implement per 4.4-E2E-002
    # - for chunk in client.chat.completions.create(
    #       model="glm-4",
    #       messages=[{"role":"user","content":"Tell me a haiku."}],
    #       stream=True,
    #       stream_options={"include_usage": True},
    #   ): ...
    # - tail chunk's usage non-None
    # - for fixture in FIXTURES (~10 prompts):
    #     abs(adapter_usage - upstream_usage) / upstream_usage < 0.01
    pytest.fail("Not implemented: 4.4-E2E-002")
