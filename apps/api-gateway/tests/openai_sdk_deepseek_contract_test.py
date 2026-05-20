"""Story 4.1 — DeepSeek Adapter — OpenAI Python SDK contract tests.

Story 4.8 T4.1 — skeleton `pytest.fail("Not implemented")` bodies CONVERTED to
executable assertions consuming the shared protocol invariants library
(_protocol_invariants.assert_chat_completion_shape / chunk_shape).

Coverage matrix:
  4.1-CONTRACT-001 — AC1 non-streaming + OQ5 cost-header omission
  4.1-CONTRACT-002 — AC2 streaming happy path (chunk-shape invariants)
  4.1-CONTRACT-003 — AC2 streaming tail-usage chunk (BR-3.3 invariant)
  4.1-CONTRACT-004 — AC1 unknown model id → 400 envelope
  4.1-CONTRACT-005 — AC2 mid-stream upstream interruption (best-effort)
  4.1-CONTRACT-006 — BR-1.5 request-id continuity through the gateway

SDK pin: openai==1.40.* (Story 3.3 BR-3.2)
Env-var guard: HE_API_TEST_GATEWAY_URL (skipped otherwise).
"""

import os
import re

import pytest
from openai import OpenAI

from _protocol_invariants import (
    REQUEST_ID_RE,
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
)

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
def test_chat_completions_deepseek_nonstream_happy_path():
    """SDK Pydantic-strict shape conformance for stream=False. Also asserts
    X-He-Cost-Usd header is ABSENT (Architect Round 2 OQ5 binding — cost
    calculation lands in Epic 7 billing-svc, NOT Story 4.1).
    """
    raw = _client().chat.completions.with_raw_response.create(
        model="deepseek-v3",
        messages=[{"role": "user", "content": "Hello"}],
        stream=False,
    )
    response = raw.parse()
    assert_chat_completion_shape(response, expected_model="deepseek-v3")

    # Vendor-specific layer
    selected = raw.headers.get("x-he-selected-model") or raw.headers.get("X-He-Selected-Model")
    assert selected == "deepseek-v3", f"x-he-selected-model={selected!r}"
    req_id = raw.headers.get("x-he-request-id") or raw.headers.get("X-He-Request-Id")
    assert req_id and REQUEST_ID_RE.match(req_id), f"x-he-request-id={req_id!r}"
    cost_header = raw.headers.get("x-he-cost-usd") or raw.headers.get("X-He-Cost-Usd")
    assert cost_header is None, f"OQ5 binding violation: x-he-cost-usd present ({cost_header!r})"


# Scenario: 4.1-CONTRACT-004 — Priority P0
def test_chat_completions_deepseek_unknown_model_raises_400_APIError():
    """SDK-side surfaced error for an unresolvable model id."""
    import openai

    with pytest.raises(openai.BadRequestError) as exc_info:
        _client().chat.completions.create(
            model="unknown-typo", messages=[{"role": "user", "content": "Hi"}]
        )
    code = getattr(exc_info.value, "code", None)
    assert code == "400_invalid_request", f"exc.code={code!r}"


# ============================================================
# AC2: Streaming /v1/chat/completions via DeepSeek adapter
# ============================================================

# Scenario: 4.1-CONTRACT-002 — Priority P0
def test_chat_completions_deepseek_stream_happy_path():
    """SDK streaming iterator completes without raising; chunk-shape invariants
    preserved across bootstrap / content / terminal chunks per BR-2.3 MED-2.
    """
    chunks = list(_client().chat.completions.create(
        model="deepseek-v3",
        messages=[{"role": "user", "content": "Hi"}],
        stream=True,
        stream_options={"include_usage": True},
    ))
    assert len(chunks) >= 2, f"got {len(chunks)} chunks; want >=2"
    # Shared id (BR-1.4 id-continuity)
    first_id = chunks[0].id
    for c in chunks:
        assert c.id == first_id, "BR-1.4: id MUST be threaded across chunks"
    # MED-2 three-way disambiguation
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model="deepseek-v3",
            is_bootstrap=(i == 0),
            is_terminal=(i == len(chunks) - 1),
        )


# Scenario: 4.1-CONTRACT-003 — Priority P0
def test_chat_completions_deepseek_stream_tail_usage_chunk_present():
    """LAST non-DONE chunk's chunk.usage is populated; BR-3.3 invariant holds."""
    chunks = list(_client().chat.completions.create(
        model="deepseek-v3",
        messages=[{"role": "user", "content": "Hi"}],
        stream=True,
        stream_options={"include_usage": True},
    ))
    tail = chunks[-1]
    assert tail.usage is not None, "BR-2.4: tail chunk MUST carry usage when include_usage=True"
    u = tail.usage
    assert u.total_tokens == u.prompt_tokens + u.completion_tokens, (
        f"BR-3.3 additivity violation: total={u.total_tokens} != prompt={u.prompt_tokens} + completion={u.completion_tokens}"
    )


# Scenario: 4.1-CONTRACT-005 — Priority P0
@pytest.mark.skip(
    reason="mid-stream upstream RST injection requires per-test fake-upstream mode switching; "
           "deferred to chaos suite (Epic 9)"
)
def test_chat_completions_deepseek_stream_mid_upstream_interruption_emits_SSE_error_frame():
    """Pre-emptively skipped — the canonical Story-4.8 fake-upstream does NOT
    expose a per-request RST mode (deterministic-fixture invariant per BR-1.10).
    Live-lane verification of upstream-failure paths lives under Epic 9 chaos."""


# Scenario: 4.1-CONTRACT-006 — Priority P0
def test_chat_completions_deepseek_request_id_continuity_gateway_to_adapter_hop():
    """End-to-end request-id continuity at the gateway entry middleware.

    The CI lane verifies (a) X-He-Request-Id appears on the SDK-side raw
    response with the canonical ^req_[0-9a-f]{12}$ shape; deeper gateway→
    adapter Connect-RPC hop continuity is verified by Story 4.1 Go-side
    integration tests (4.1-INT-* range)."""
    raw = _client().chat.completions.with_raw_response.create(
        model="deepseek-v3",
        messages=[{"role": "user", "content": "Hi"}],
    )
    rid = raw.headers.get("x-he-request-id") or raw.headers.get("X-He-Request-Id")
    assert rid and REQUEST_ID_RE.match(rid), f"X-He-Request-Id shape: {rid!r}"


# ============================================================
# Pin guard — keep parity with Story 3.3 / 3.4 / 3.5 SDK pin
# ============================================================
def test_sdk_pin_guard_openai_1_40():
    import openai
    assert openai.__version__.startswith("1.40."), (
        f"SDK version {openai.__version__} is not in the 1.40.x family — "
        "if intentional, update Story 3.3 BR-3.2 + Story 4.1 BR-3.2 pin guards together."
    )


# Silence unused-import warning (re is imported by the shared helper file).
_ = re
