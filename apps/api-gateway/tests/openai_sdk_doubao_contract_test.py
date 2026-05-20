"""Story 4.5 — Doubao (Volcengine Ark v3) Adapter — OpenAI Python SDK contract tests.

Story 4.8 T4.5 — skeleton bodies CONVERTED + rewired to the shared protocol
invariants library. Vendor delta layer: BR-1.11 ROUND-TRIP REWRITE — every
scenario asserts `response.model == request.model` (NEVER `ep-` prefix) per
R10 leak-prevention. The fake-upstream T1.5 echoes the endpoint id back; the
adapter back-translates via the req.Model closure (Architect Round 1 l-1
ratification — no ReverseLookup).

Coverage matrix (per Story 4.5 T2.6 — 2 models × 2 paths):
  4.5-CONTRACT-001 — AC1 non-streaming for `doubao-pro`
  4.5-CONTRACT-002 — AC1 non-streaming for `doubao-lite`
  4.5-CONTRACT-003 — AC2 streaming for `doubao-pro` + tail-usage + per-chunk back-translate
  4.5-CONTRACT-004 — AC2 streaming for `doubao-lite`

SDK pin: openai==1.40.* (REUSE Story 3.3 BR-3.2)
"""

import os

import pytest
from openai import OpenAI

from _protocol_invariants import (
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
)

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


def _nonstream_round_trip(model: str):
    response = _client().chat.completions.create(
        model=model, messages=[{"role": "user", "content": "Hello"}], stream=False,
    )
    assert_chat_completion_shape(response, expected_model=model)
    # R10 leak-prevention — adapter MUST back-translate ep-... → friendly id.
    assert not response.model.startswith("ep-"), (
        f"BR-1.11 leak: response.model={response.model!r} carries Volcengine endpoint id"
    )


def _stream_round_trip(model: str):
    chunks = list(_client().chat.completions.create(
        model=model, messages=[{"role": "user", "content": "Hi"}],
        stream=True, stream_options={"include_usage": True},
    ))
    assert len(chunks) >= 2
    # EVERY chunk.model == friendly id (NEVER ep- prefix) — BR-1.11 per-chunk back-translate.
    for c in chunks:
        assert c.model == model, f"BR-1.11 per-chunk leak: chunk.model={c.model!r}"
        assert not c.model.startswith("ep-")
    # Shape per chunk via MED-2 three-way.
    for i, chunk in enumerate(chunks):
        assert_chat_completion_chunk_shape(
            chunk, expected_model=model,
            is_bootstrap=(i == 0), is_terminal=(i == len(chunks) - 1),
        )


# ============================================================
# 4.5-CONTRACT-001..004
# ============================================================


def test_doubao_pro_nonstreaming():
    _nonstream_round_trip(DOUBAO_PRO)


def test_doubao_lite_nonstreaming():
    _nonstream_round_trip(DOUBAO_LITE)


def test_doubao_pro_streaming():
    _stream_round_trip(DOUBAO_PRO)


def test_doubao_lite_streaming():
    _stream_round_trip(DOUBAO_LITE)
