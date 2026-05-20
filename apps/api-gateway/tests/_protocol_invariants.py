"""Story 4.8 — Shared OpenAI-protocol invariants library (AC2 / T0.1 + T0.2).

Single source of truth for OpenAI-canonical response/chunk/model/embedding/error
envelope shape assertions. Imported by EVERY per-vendor + per-endpoint contract
test under apps/api-gateway/tests/ so the per-file copy-paste pattern stays out
of the test bodies.

Scope rules per Story 4.8 BR-2.1 / BR-2.11:
  - Single module (leading underscore signals "not a test module" to pytest).
  - Stdlib-only imports (`re`, `time`, `json`). MUST NOT import `openai`.
  - Helpers are pure functions; they accept ALREADY-PARSED dicts or SDK
    Pydantic objects via duck-typed attribute access. They do NOT perform HTTP.

The five exported helpers (BR-2.2 / BR-2.3 / BR-2.3b / BR-2.4 / BR-2.5):
  - assert_chat_completion_shape(response, expected_model)
  - assert_chat_completion_chunk_shape(chunk, expected_model, *,
        is_bootstrap=False, is_terminal=False)   (BR-2.3 MED-2 three-way)
  - assert_model_entry_shape(entry, *, expected_id_set=None)  (MED-3 lifted)
  - assert_embedding_shape(response, expected_model)
  - assert_error_envelope_shape(response_body, expected_code, expected_status)
"""

from __future__ import annotations

import re
import time

# --- Canonical constants (mirror rest-api-spec.md §5.1.1 / §5.1.1.1 / §5.1.2) --

CHATCMPL_ID_RE = re.compile(r"^chatcmpl-")
REQUEST_ID_RE = re.compile(r"^req_[0-9a-f]{12}$")
CANONICAL_FINISH_REASONS = frozenset({"stop", "length", "tool_calls", "content_filter"})
CANONICAL_ERROR_TYPES = frozenset({"invalid_request_error", "server_error"})

_CLOCK_SKEW_SECONDS = 60


# --- Duck-typed access helpers --------------------------------------------------


def _get(obj, key, default=None):
    """Read a field from either a dict or an attribute-bearing object."""
    if obj is None:
        return default
    if isinstance(obj, dict):
        return obj.get(key, default)
    return getattr(obj, key, default)


def _now_with_skew() -> int:
    return int(time.time()) + _CLOCK_SKEW_SECONDS


# --- Helper 1: chat.completion shape (BR-2.2) -----------------------------------


def assert_chat_completion_shape(response, expected_model: str) -> None:
    """Verify a non-streaming /v1/chat/completions response shape (BR-2.2).

    Accepts the OpenAI Python SDK ChatCompletion object OR a plain dict.
    Raises AssertionError on the FIRST invariant violation with a message
    that names the violated clause (e.g., "BR-2.2(j) total != prompt + completion").
    """
    rid = _get(response, "id")
    assert isinstance(rid, str) and CHATCMPL_ID_RE.match(rid), (
        f"BR-2.2(a) id MUST be non-empty string matching {CHATCMPL_ID_RE.pattern!r}; got {rid!r}"
    )

    obj = _get(response, "object")
    assert obj == "chat.completion", f"BR-2.2(b) object MUST be 'chat.completion'; got {obj!r}"

    created = _get(response, "created")
    assert isinstance(created, int) and created > 0 and created <= _now_with_skew(), (
        f"BR-2.2(c) created MUST be positive int ≤ now+60s; got {created!r}"
    )

    model = _get(response, "model")
    assert model == expected_model, f"BR-2.2(d) model mismatch: got {model!r}, want {expected_model!r}"

    choices = _get(response, "choices")
    assert isinstance(choices, list) and len(choices) > 0, (
        f"BR-2.2(e) choices MUST be non-empty list; got {choices!r}"
    )

    c0 = choices[0]
    assert _get(c0, "index") == 0, f"BR-2.2(f) choices[0].index MUST be 0; got {_get(c0, 'index')!r}"

    message = _get(c0, "message")
    assert message is not None, "BR-2.2(g) choices[0].message MUST exist"
    role = _get(message, "role")
    assert role == "assistant", f"BR-2.2(g) message.role MUST be 'assistant'; got {role!r}"

    content = _get(message, "content")
    assert isinstance(content, str) and len(content) > 0, (
        f"BR-2.2(h) message.content MUST be non-empty string; got {content!r}"
    )

    finish_reason = _get(c0, "finish_reason")
    assert finish_reason in CANONICAL_FINISH_REASONS, (
        f"BR-2.2(i) finish_reason {finish_reason!r} not in {sorted(CANONICAL_FINISH_REASONS)}"
    )

    usage = _get(response, "usage")
    assert usage is not None, "BR-2.2(j) usage MUST be populated"
    _assert_usage_triple(usage, allow_zero_completion=False)


def _assert_usage_triple(usage, *, allow_zero_completion: bool) -> None:
    """Validate the (prompt, completion, total) additivity invariant (Story 4.1 BR-3.3).

    allow_zero_completion: streaming-tail chunks for very-short responses may
    legitimately emit completion_tokens=0; non-streaming responses must not.
    """
    prompt = _get(usage, "prompt_tokens")
    completion = _get(usage, "completion_tokens")
    total = _get(usage, "total_tokens")
    assert isinstance(prompt, int) and prompt > 0, (
        f"BR-2.2(j) usage.prompt_tokens MUST be positive int; got {prompt!r}"
    )
    if allow_zero_completion:
        assert isinstance(completion, int) and completion >= 0, (
            f"BR-2.2(j) usage.completion_tokens MUST be non-negative int; got {completion!r}"
        )
    else:
        assert isinstance(completion, int) and completion > 0, (
            f"BR-2.2(j) usage.completion_tokens MUST be positive int; got {completion!r}"
        )
    assert isinstance(total, int), f"BR-2.2(j) usage.total_tokens MUST be int; got {total!r}"
    assert total == prompt + completion, (
        f"BR-2.2(j) Story-4.1 BR-3.3 invariant violation: total={total} != prompt={prompt} + completion={completion}"
    )


# --- Helper 2: chat.completion.chunk shape (BR-2.3 + MED-2 + MED-5) -------------


def assert_chat_completion_chunk_shape(
    chunk,
    expected_model: str,
    *,
    is_bootstrap: bool = False,
    is_terminal: bool = False,
) -> None:
    """Verify a streaming chunk shape (BR-2.3) with MED-2 three-way disambiguation.

    is_bootstrap / is_terminal flags are MUTUALLY EXCLUSIVE per the MED-2 ratification.
    Callers pass:
      - is_bootstrap=True for chunk index 0 (role-only delta);
      - is_terminal=True for the final non-DONE chunk (finish_reason + usage);
      - NEITHER for content chunks in between (content-only delta).

    MED-5 fold-in (Architect Round 2): on terminal chunks, the usage triple is
    only asserted when it is present; callers that did NOT set
    `stream_options.include_usage=True` (e.g., Story-3.4 mock chunker) will
    legitimately emit `usage=None` on the terminal chunk.
    """
    assert not (is_bootstrap and is_terminal), (
        "is_bootstrap and is_terminal are mutually exclusive"
    )

    cid = _get(chunk, "id")
    assert isinstance(cid, str) and CHATCMPL_ID_RE.match(cid), (
        f"BR-2.3(a) chunk.id MUST match {CHATCMPL_ID_RE.pattern!r}; got {cid!r}"
    )

    obj = _get(chunk, "object")
    assert obj == "chat.completion.chunk", (
        f"BR-2.3(b) chunk.object MUST be 'chat.completion.chunk'; got {obj!r}"
    )

    created = _get(chunk, "created")
    assert isinstance(created, int) and created > 0 and created <= _now_with_skew(), (
        f"BR-2.3(c) chunk.created MUST be positive int ≤ now+60s; got {created!r}"
    )

    model = _get(chunk, "model")
    assert model == expected_model, (
        f"BR-2.3(d) chunk.model mismatch: got {model!r}, want {expected_model!r}"
    )

    choices = _get(chunk, "choices")
    assert isinstance(choices, list) and len(choices) > 0, (
        f"BR-2.3(e) chunk.choices MUST be non-empty list; got {choices!r}"
    )
    c0 = choices[0]
    assert _get(c0, "index") == 0, (
        f"BR-2.3(e) chunk.choices[0].index MUST be 0; got {_get(c0, 'index')!r}"
    )

    delta = _get(c0, "delta")
    assert delta is not None, "BR-2.3(f) chunk.choices[0].delta MUST exist"

    if is_bootstrap:
        role = _get(delta, "role")
        assert role == "assistant", (
            f"BR-2.3(g) bootstrap chunk: delta.role MUST be 'assistant'; got {role!r}"
        )
    elif is_terminal:
        finish_reason = _get(c0, "finish_reason")
        assert finish_reason == "stop", (
            f"BR-2.3(i) terminal chunk: finish_reason MUST be 'stop'; got {finish_reason!r}"
        )
        # MED-5 fold-in: usage assertion is conditional on presence.
        usage = _get(chunk, "usage")
        if usage is not None:
            _assert_usage_triple(usage, allow_zero_completion=True)
    else:
        content = _get(delta, "content")
        assert isinstance(content, str) and len(content) > 0, (
            f"BR-2.3(h) content chunk: delta.content MUST be non-empty string; got {content!r}"
        )


# --- Helper 3: model catalogue entry shape (BR-2.3b — MED-3 lifted) -------------


def assert_model_entry_shape(entry, *, expected_id_set=None) -> None:
    """Verify a /v1/models data[] entry shape (BR-2.3b — MED-3 lifted to shared lib).

    expected_id_set: when provided (non-None), verify entry.id ∈ expected_id_set.
    Used by T3.4 + T4.8 to enforce full registry membership in one parametrised invocation.
    """
    eid = _get(entry, "id")
    assert isinstance(eid, str) and len(eid) > 0, (
        f"BR-2.3b(a) entry.id MUST be non-empty string; got {eid!r}"
    )

    obj = _get(entry, "object")
    assert obj == "model", f"BR-2.3b(b) entry.object MUST be 'model'; got {obj!r}"

    created = _get(entry, "created")
    assert isinstance(created, int) and created > 0 and created <= _now_with_skew(), (
        f"BR-2.3b(c) entry.created MUST be positive int ≤ now+60s; got {created!r}"
    )

    owned_by = _get(entry, "owned_by")
    assert isinstance(owned_by, str) and len(owned_by) > 0, (
        f"BR-2.3b(d) entry.owned_by MUST be non-empty string; got {owned_by!r}"
    )

    # Story 4.7 capabilities extension: under the OpenAI SDK, unknown fields
    # land in `model_extra` (Pydantic extra="allow"); under raw httpx, they
    # land at the top level of the dict.
    caps = _get(entry, "capabilities")
    if caps is None:
        extras = _get(entry, "model_extra") or {}
        caps = extras.get("capabilities") if isinstance(extras, dict) else None
    assert isinstance(caps, dict), (
        f"BR-2.3b(e) entry.capabilities MUST exist as dict (Story-4.7); got {caps!r}"
    )

    if expected_id_set is not None:
        assert eid in expected_id_set, (
            f"BR-2.3b(f) entry.id {eid!r} not in expected_id_set {sorted(expected_id_set)!r}"
        )


# --- Helper 4: embeddings response shape (BR-2.4) -------------------------------


def assert_embedding_shape(response, expected_model: str) -> None:
    """Verify a /v1/embeddings response shape (BR-2.4)."""
    obj = _get(response, "object")
    assert obj == "list", f"BR-2.4(a) response.object MUST be 'list'; got {obj!r}"

    data = _get(response, "data")
    assert isinstance(data, list) and len(data) > 0, (
        f"BR-2.4(b) response.data MUST be non-empty list; got {data!r}"
    )

    dim = None
    for i, item in enumerate(data):
        item_obj = _get(item, "object")
        assert item_obj == "embedding", (
            f"BR-2.4(c) data[{i}].object MUST be 'embedding'; got {item_obj!r}"
        )
        idx = _get(item, "index")
        assert idx == i, f"BR-2.4(d) data[{i}].index MUST be {i}; got {idx!r}"
        embedding = _get(item, "embedding")
        assert isinstance(embedding, list) and len(embedding) > 0, (
            f"BR-2.4(e) data[{i}].embedding MUST be non-empty list[float]; got {embedding!r}"
        )
        if dim is None:
            dim = len(embedding)
        else:
            assert len(embedding) == dim, (
                f"BR-2.4(e) data[{i}].embedding length {len(embedding)} != dim {dim}"
            )
        for j, v in enumerate(embedding):
            assert isinstance(v, (int, float)) and not isinstance(v, bool), (
                f"BR-2.4(e) data[{i}].embedding[{j}] MUST be number; got {v!r}"
            )

    model = _get(response, "model")
    assert model == expected_model, (
        f"BR-2.4(f) response.model mismatch: got {model!r}, want {expected_model!r}"
    )

    usage = _get(response, "usage")
    assert usage is not None, "BR-2.4(g) response.usage MUST be populated"
    prompt = _get(usage, "prompt_tokens")
    total = _get(usage, "total_tokens")
    assert isinstance(prompt, int) and prompt > 0, (
        f"BR-2.4(g) usage.prompt_tokens MUST be positive int; got {prompt!r}"
    )
    assert total == prompt, (
        f"BR-2.4(g) Story-3.5 BR-2.5 embeddings invariant: total={total} != prompt={prompt}"
    )


# --- Helper 5: error envelope shape (BR-2.5) ------------------------------------


_REQUIRED_ERROR_KEYS = frozenset({"code", "message", "type", "param", "he_request_id"})


def assert_error_envelope_shape(response_body, expected_code: str, expected_status: int) -> None:
    """Verify Story 3.6 5-field error envelope shape (BR-2.5).

    response_body: parsed JSON dict (NOT raw bytes).
    expected_code: e.g. "401_invalid_api_key".
    expected_status: e.g. 401. Asserted self-consistently against the
        expected_code prefix; callers verify HTTP status separately.
    """
    assert isinstance(response_body, dict), (
        f"BR-2.5(a) response body MUST be a JSON object; got {type(response_body).__name__}"
    )
    top_keys = set(response_body.keys())
    assert top_keys == {"error"}, (
        f"BR-2.5(a) body MUST have single top-level key 'error'; got keys {sorted(top_keys)}"
    )

    error = response_body["error"]
    assert isinstance(error, dict), f"BR-2.5(b) error MUST be a dict; got {type(error).__name__}"
    actual_keys = set(error.keys())
    assert actual_keys == _REQUIRED_ERROR_KEYS, (
        f"BR-2.5(b) error MUST have EXACTLY 5 keys {sorted(_REQUIRED_ERROR_KEYS)}; "
        f"got {sorted(actual_keys)}"
    )

    code = error["code"]
    assert code == expected_code, f"BR-2.5(c) error.code mismatch: got {code!r}, want {expected_code!r}"

    message = error["message"]
    assert isinstance(message, str) and len(message) > 0, (
        f"BR-2.5(d) error.message MUST be non-empty string; got {message!r}"
    )

    etype = error["type"]
    assert etype in CANONICAL_ERROR_TYPES, (
        f"BR-2.5(e) error.type {etype!r} not in {sorted(CANONICAL_ERROR_TYPES)}"
    )

    # BR-2.5(f) canonical mapping: 4xx → invalid_request_error; 5xx → server_error.
    status_prefix = expected_code.split("_", 1)[0]
    assert status_prefix.isdigit() and len(status_prefix) == 3, (
        f"BR-2.5(f) expected_code MUST start with a 3-digit status prefix; got {expected_code!r}"
    )
    code_status = int(status_prefix)
    assert code_status == expected_status, (
        f"BR-2.5(i) expected_status {expected_status} mismatches code prefix {code_status}"
    )
    if 400 <= code_status < 500:
        assert etype == "invalid_request_error", (
            f"BR-2.5(f) 4xx code {code!r} MUST map to 'invalid_request_error'; got {etype!r}"
        )
    elif 500 <= code_status < 600:
        assert etype == "server_error", (
            f"BR-2.5(f) 5xx code {code!r} MUST map to 'server_error'; got {etype!r}"
        )

    param = error["param"]
    assert param is None or (isinstance(param, str) and len(param) > 0), (
        f"BR-2.5(g) error.param MUST be null or non-empty string; got {param!r}"
    )

    he_request_id = error["he_request_id"]
    assert isinstance(he_request_id, str) and REQUEST_ID_RE.match(he_request_id), (
        f"BR-2.5(h) error.he_request_id MUST match {REQUEST_ID_RE.pattern!r}; got {he_request_id!r}"
    )
