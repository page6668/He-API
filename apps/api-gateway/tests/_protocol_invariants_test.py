"""Story 4.8 T0.3 — meta-tests for the shared protocol invariants library.

Covers scenarios 4.8-UNIT-001..020 from docs/qa/assessments/4.8-test-design-20260520.md.
Stdlib-only (no openai SDK). Runs as part of every `pytest apps/api-gateway/tests/`
invocation and DOES NOT require HE_API_TEST_GATEWAY_URL (no network I/O).
"""

from __future__ import annotations

import time

import pytest

from _protocol_invariants import (
    CANONICAL_ERROR_TYPES,
    CANONICAL_FINISH_REASONS,
    CHATCMPL_ID_RE,
    REQUEST_ID_RE,
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
    assert_embedding_shape,
    assert_error_envelope_shape,
    assert_model_entry_shape,
)


# ---------------------------------------------------------------------------
# 4.8-UNIT-001 — constants vs §5.1.1.1 / §5.1.2 spec verbatim (R1)
# ---------------------------------------------------------------------------


def test_4_8_unit_001_constants_match_spec_verbatim():
    """Drift here corrupts every downstream consumer — guard at the constants layer."""
    assert CHATCMPL_ID_RE.pattern == "^chatcmpl-"
    assert REQUEST_ID_RE.pattern == "^req_[0-9a-f]{12}$"
    assert CANONICAL_FINISH_REASONS == frozenset(
        {"stop", "length", "tool_calls", "content_filter"}
    )
    assert CANONICAL_ERROR_TYPES == frozenset({"invalid_request_error", "server_error"})


# ---------------------------------------------------------------------------
# Fixture builders — handy templates the tests mutate per-scenario
# ---------------------------------------------------------------------------


def _fresh_created() -> int:
    return int(time.time())


def _chat_completion(model="qwen-max", **overrides):
    base = {
        "id": "chatcmpl-fake-deadbeef",
        "object": "chat.completion",
        "created": _fresh_created(),
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": "Hello"},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
    }
    base.update(overrides)
    return base


def _chunk(model="qwen-max", *, delta=None, finish_reason=None, usage=None, **overrides):
    base = {
        "id": "chatcmpl-fake-stream-cafef00d",
        "object": "chat.completion.chunk",
        "created": _fresh_created(),
        "model": model,
        "choices": [
            {
                "index": 0,
                "delta": delta if delta is not None else {},
                "finish_reason": finish_reason,
            }
        ],
        "usage": usage,
    }
    base.update(overrides)
    return base


def _model_entry(model_id="qwen-max", **overrides):
    base = {
        "id": model_id,
        "object": "model",
        "created": _fresh_created(),
        "owned_by": "alibaba",
        "capabilities": {
            "chat": True,
            "streaming": True,
            "function_calling": False,
            "vision": False,
            "json_mode": False,
            "context_window_tokens": 32768,
            "max_output_tokens": 8192,
        },
    }
    base.update(overrides)
    return base


def _embedding(model="text-embedding-3-small", n=1, dim=128):
    data = [
        {"object": "embedding", "index": i, "embedding": [0.0] * dim} for i in range(n)
    ]
    return {
        "object": "list",
        "data": data,
        "model": model,
        "usage": {"prompt_tokens": 3, "total_tokens": 3},
    }


def _envelope(code="401_invalid_api_key", **overrides):
    error = {
        "code": code,
        "message": "missing bearer token",
        "type": "invalid_request_error",
        "param": None,
        "he_request_id": "req_0123456789ab",
    }
    error.update(overrides)
    return {"error": error}


# ---------------------------------------------------------------------------
# Helper 1 — assert_chat_completion_shape (4.8-UNIT-002..004 + 4.8-UNIT-012)
# ---------------------------------------------------------------------------


def test_4_8_unit_002_assert_chat_completion_shape_happy_path():
    assert_chat_completion_shape(_chat_completion(model="deepseek-v3"), expected_model="deepseek-v3")


def test_4_8_unit_003_assert_chat_completion_rejects_missing_usage_triple():
    body = _chat_completion()
    body["usage"] = None
    with pytest.raises(AssertionError, match="BR-2.2.j. usage MUST be populated"):
        assert_chat_completion_shape(body, expected_model="qwen-max")


def test_4_8_unit_004_assert_chat_completion_rejects_wrong_role():
    body = _chat_completion()
    body["choices"][0]["message"]["role"] = "system"
    with pytest.raises(AssertionError, match="BR-2.2.g. message.role MUST be 'assistant'"):
        assert_chat_completion_shape(body, expected_model="qwen-max")


def test_4_8_unit_012_assert_chat_completion_rejects_off_by_one_total():
    """4.8-UNIT-012 [BLIND-SPOT BOUNDARY-001] — Story-4.1 BR-3.3 additivity invariant."""
    body = _chat_completion()
    body["usage"]["total_tokens"] = body["usage"]["prompt_tokens"] + body["usage"]["completion_tokens"] + 1
    with pytest.raises(AssertionError, match="BR-3.3 invariant violation"):
        assert_chat_completion_shape(body, expected_model="qwen-max")


def test_assert_chat_completion_rejects_wrong_model():
    body = _chat_completion(model="qwen-max")
    with pytest.raises(AssertionError, match="BR-2.2.d. model mismatch"):
        assert_chat_completion_shape(body, expected_model="deepseek-v3")


def test_assert_chat_completion_rejects_unknown_finish_reason():
    body = _chat_completion()
    body["choices"][0]["finish_reason"] = "abracadabra"
    with pytest.raises(AssertionError, match="BR-2.2.i. finish_reason"):
        assert_chat_completion_shape(body, expected_model="qwen-max")


def test_assert_chat_completion_rejects_bad_chatcmpl_id():
    body = _chat_completion()
    body["id"] = "completion-not-prefixed"
    with pytest.raises(AssertionError, match=r"BR-2.2\(a\)"):
        assert_chat_completion_shape(body, expected_model="qwen-max")


# ---------------------------------------------------------------------------
# Helper 2 — assert_chat_completion_chunk_shape (4.8-UNIT-005..008)
# ---------------------------------------------------------------------------


def test_4_8_unit_005_chunk_helper_mutually_exclusive_flags():
    """4.8-UNIT-005 [BLIND-SPOT BOUNDARY-001] — MED-2 mutual-exclusion guard."""
    chunk = _chunk(delta={"role": "assistant"})
    with pytest.raises(AssertionError, match="is_bootstrap and is_terminal are mutually exclusive"):
        assert_chat_completion_chunk_shape(
            chunk, expected_model="qwen-max", is_bootstrap=True, is_terminal=True
        )


def test_4_8_unit_006_chunk_helper_bootstrap_happy_path():
    chunk = _chunk(delta={"role": "assistant"})
    assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max", is_bootstrap=True)


def test_4_8_unit_007_chunk_helper_terminal_with_usage_happy_path():
    chunk = _chunk(
        delta={},
        finish_reason="stop",
        usage={"prompt_tokens": 5, "completion_tokens": 4, "total_tokens": 9},
    )
    assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max", is_terminal=True)


def test_4_8_unit_008_chunk_helper_content_chunk_happy_path():
    chunk = _chunk(delta={"content": "Hello"})
    assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max")


def test_chunk_helper_terminal_without_usage_med_5_fold_in():
    """MED-5 fold-in: mock-chunker emits NO usage on terminal; helper tolerates."""
    chunk = _chunk(model="he-mock", delta={}, finish_reason="stop", usage=None)
    assert_chat_completion_chunk_shape(chunk, expected_model="he-mock", is_terminal=True)


def test_chunk_helper_terminal_rejects_wrong_finish_reason():
    chunk = _chunk(delta={}, finish_reason="length", usage=None)
    with pytest.raises(AssertionError, match="BR-2.3.i. terminal chunk"):
        assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max", is_terminal=True)


def test_chunk_helper_content_rejects_empty_delta():
    chunk = _chunk(delta={})
    with pytest.raises(AssertionError, match="BR-2.3.h. content chunk"):
        assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max")


def test_chunk_helper_rejects_wrong_object():
    chunk = _chunk(delta={"content": "Hi"})
    chunk["object"] = "chat.completion"
    with pytest.raises(AssertionError, match=r"BR-2.3\(b\)"):
        assert_chat_completion_chunk_shape(chunk, expected_model="qwen-max")


# ---------------------------------------------------------------------------
# Helper 3 — assert_model_entry_shape (4.8-UNIT-009..011)
# ---------------------------------------------------------------------------


def test_4_8_unit_009_model_entry_happy_path():
    assert_model_entry_shape(_model_entry())


def test_4_8_unit_010_model_entry_rejects_id_outside_expected_set():
    entry = _model_entry(model_id="qwen-max")
    with pytest.raises(AssertionError, match=r"BR-2.3b\(f\)"):
        assert_model_entry_shape(entry, expected_id_set={"deepseek-v3", "ernie-4.0"})


def test_4_8_unit_011_model_entry_happy_with_expected_id_set_none():
    """BR-2.3b(f) — None branch skips containment."""
    assert_model_entry_shape(_model_entry(model_id="any-id"), expected_id_set=None)


def test_model_entry_rejects_missing_capabilities():
    entry = _model_entry()
    del entry["capabilities"]
    with pytest.raises(AssertionError, match=r"BR-2.3b\(e\)"):
        assert_model_entry_shape(entry)


def test_model_entry_accepts_capabilities_under_model_extra():
    """SDK Pydantic round-trip stows extras under model_extra dict."""

    class _Obj:
        def __init__(self):
            self.id = "qwen-max"
            self.object = "model"
            self.created = _fresh_created()
            self.owned_by = "alibaba"
            self.capabilities = None  # not present as attribute
            self.model_extra = {"capabilities": {"chat": True}}

    assert_model_entry_shape(_Obj())


# ---------------------------------------------------------------------------
# Helper 4 — assert_embedding_shape (4.8-UNIT-013..014)
# ---------------------------------------------------------------------------


def test_4_8_unit_013_embedding_happy_128_dim():
    assert_embedding_shape(_embedding(dim=128), expected_model="text-embedding-3-small")


def test_4_8_unit_014_embedding_rejects_unequal_lengths():
    resp = _embedding(n=2, dim=128)
    resp["data"][1]["embedding"] = [0.0] * 64
    with pytest.raises(AssertionError, match=r"BR-2.4\(e\)"):
        assert_embedding_shape(resp, expected_model="text-embedding-3-small")


def test_embedding_rejects_zero_prompt_tokens():
    resp = _embedding()
    resp["usage"]["prompt_tokens"] = 0
    resp["usage"]["total_tokens"] = 0
    with pytest.raises(AssertionError, match=r"BR-2.4\(g\)"):
        assert_embedding_shape(resp, expected_model="text-embedding-3-small")


def test_embedding_rejects_total_neq_prompt():
    resp = _embedding()
    resp["usage"]["total_tokens"] = resp["usage"]["prompt_tokens"] + 1
    with pytest.raises(AssertionError, match="embeddings invariant"):
        assert_embedding_shape(resp, expected_model="text-embedding-3-small")


# ---------------------------------------------------------------------------
# Helper 5 — assert_error_envelope_shape (4.8-UNIT-015..019)
# ---------------------------------------------------------------------------


def test_4_8_unit_015_envelope_happy_path_5_field():
    assert_error_envelope_shape(_envelope(), expected_code="401_invalid_api_key", expected_status=401)


def test_4_8_unit_016_envelope_rejects_six_keys():
    """4.8-UNIT-016 [BLIND-SPOT BOUNDARY-004] — EXACTLY-5 keys discipline."""
    env = _envelope()
    env["error"]["extra_field"] = "leak"
    with pytest.raises(AssertionError, match=r"BR-2.5\(b\)"):
        assert_error_envelope_shape(env, expected_code="401_invalid_api_key", expected_status=401)


def test_4_8_unit_017_envelope_rejects_four_keys():
    env = _envelope()
    del env["error"]["param"]
    with pytest.raises(AssertionError, match=r"BR-2.5\(b\)"):
        assert_error_envelope_shape(env, expected_code="401_invalid_api_key", expected_status=401)


def test_4_8_unit_018_envelope_rejects_4xx_code_with_server_error_type():
    env = _envelope()
    env["error"]["type"] = "server_error"
    with pytest.raises(AssertionError, match=r"BR-2.5\(f\) 4xx"):
        assert_error_envelope_shape(env, expected_code="401_invalid_api_key", expected_status=401)


def test_4_8_unit_019_envelope_rejects_wrong_he_request_id_shape():
    env = _envelope()
    env["error"]["he_request_id"] = "req_XYZ"
    with pytest.raises(AssertionError, match=r"BR-2.5\(h\)"):
        assert_error_envelope_shape(env, expected_code="401_invalid_api_key", expected_status=401)


def test_envelope_rejects_status_mismatch_with_code_prefix():
    """BR-2.5(i) self-consistency: expected_status MUST match code prefix."""
    with pytest.raises(AssertionError, match=r"BR-2.5\(i\)"):
        assert_error_envelope_shape(_envelope(), expected_code="401_invalid_api_key", expected_status=400)


def test_envelope_accepts_5xx_with_server_error_type():
    env = _envelope(code="502_upstream_unavailable", type="server_error", message="upstream down")
    assert_error_envelope_shape(env, expected_code="502_upstream_unavailable", expected_status=502)


def test_envelope_rejects_extra_top_level_key():
    env = _envelope()
    env["meta"] = "leak"
    with pytest.raises(AssertionError, match=r"BR-2.5\(a\)"):
        assert_error_envelope_shape(env, expected_code="401_invalid_api_key", expected_status=401)


# ---------------------------------------------------------------------------
# 4.8-UNIT-020 — BR-2.11: stdlib-only, no openai SDK dependency
# ---------------------------------------------------------------------------


def test_4_8_unit_020_module_has_no_openai_dependency():
    """Verifies BR-2.11 — `import _protocol_invariants` MUST succeed without openai installed."""
    import sys

    import _protocol_invariants as mod  # already imported above; this is the runtime check

    file_text = open(mod.__file__).read()
    assert "import openai" not in file_text, "BR-2.11: _protocol_invariants MUST NOT import openai"
    # Module imports only stdlib symbols at runtime.
    for name in ("re", "time"):
        assert name in sys.modules, f"stdlib {name} expected to be already imported by the helper"
