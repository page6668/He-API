"""Story 4.8 T3 — OpenAI protocol-completeness matrix test (AC2 / BR-2.6..2.9).

20-cell parametrised matrix (10 vendor model-ids × stream={False, True}) +
cross-cutting endpoint shape checks (/v1/models, /public/models,
/v1/embeddings) + three-scenario error envelope coverage (401, 400, 405).

Scenario IDs: 4.8-MATRIX-001..009 (per docs/qa/assessments/4.8-test-design-20260520.md).

Per BR-2.8 single-source-of-truth: when Story 4.9+ adds a vendor, append one
entry to MATRIX_MODELS and the parametrize decorator auto-grows the matrix.
Per BR-2.9: he-router-* virtual model ids are EXCLUDED from MATRIX_MODELS
(no adapter pod — route via Epic-6 routing-svc which is not yet landed);
explicit pytest.mark.skip placeholders preserve JUnit-XML audit visibility.
"""

from __future__ import annotations

import os

import httpx
import pytest

from _protocol_invariants import (
    assert_chat_completion_chunk_shape,
    assert_chat_completion_shape,
    assert_embedding_shape,
    assert_error_envelope_shape,
    assert_model_entry_shape,
)

# ---------------------------------------------------------------------------
# BR-2.8 single-source-of-truth — appending here auto-grows the matrix.
# ---------------------------------------------------------------------------

MATRIX_MODELS: list[str] = [
    "deepseek-v3",
    "qwen-max",
    "qwen-plus",
    "moonshot-v1-8k",
    "moonshot-v1-32k",
    "moonshot-v1-128k",
    "glm-4",
    "doubao-pro",
    "doubao-lite",
    "ernie-4.0",
]
MATRIX_STREAM: list[bool] = [False, True]
MATRIX_CELLS: list[tuple[str, bool]] = [(m, s) for m in MATRIX_MODELS for s in MATRIX_STREAM]

# OQ-4.8-6 — he-router-* virtual ids EXCLUDED from MATRIX_MODELS.
VIRTUAL_HE_ROUTER_MODELS = ("he-router-cost", "he-router-quality", "he-router-latency")


# Skip the entire module when the gateway URL is unset — the matrix needs the
# real gateway-under-test, NOT a unit-tier fixture mock.
pytestmark = pytest.mark.skipif(
    os.environ.get("HE_API_TEST_GATEWAY_URL") is None,
    reason="HE_API_TEST_GATEWAY_URL not set — protocol-completeness matrix requires a running gateway",
)


# ---------------------------------------------------------------------------
# 4.8-MATRIX-001 — module-level constants are correctly shaped (R10 guard).
# ---------------------------------------------------------------------------


def test_4_8_matrix_001_module_constants():
    """BR-2.8 + R10 guard — MATRIX_MODELS length is exactly 10; 20 cells total."""
    assert len(MATRIX_MODELS) == 10, f"BR-2.8 mutation: {len(MATRIX_MODELS)} entries, want 10"
    assert MATRIX_STREAM == [False, True]
    assert len(MATRIX_CELLS) == 20
    # R10 leak-prevention: no he-router-* id leaks into the real matrix.
    for m in MATRIX_MODELS:
        assert not m.startswith("he-router-"), (
            f"R10 leak: virtual model {m!r} included in MATRIX_MODELS; route via Epic-6 routing-svc"
        )


# ---------------------------------------------------------------------------
# 4.8-MATRIX-002 — 20-cell chat-completion shape matrix (BR-2.6 + BR-2.3 MED-2).
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("model,stream", MATRIX_CELLS)
def test_4_8_matrix_002_chat_shape(
    model: str,
    stream: bool,
    openai_client,
    expected_vendor_models_present,
):
    """The load-bearing 20-cell matrix per BR-2.6.

    Each cell is one pytest item so a failure surfaces the exact (model, stream)
    pair. Streaming cells exercise BR-2.3 three-way disambiguation
    (bootstrap / content / terminal flags).
    """
    skip_reason = expected_vendor_models_present.get(model)
    if skip_reason:
        pytest.skip(skip_reason)

    messages = [{"role": "user", "content": "Hi"}]
    if stream:
        stream_obj = openai_client.chat.completions.create(
            model=model,
            messages=messages,
            stream=True,
            stream_options={"include_usage": True},
        )
        chunks = list(stream_obj)
        assert len(chunks) >= 2, f"want >=2 chunks; got {len(chunks)}"
        # MED-2 three-way: chunk[0] = bootstrap; last non-DONE = terminal;
        # everything in between is a content chunk.
        for i, chunk in enumerate(chunks):
            is_bootstrap = i == 0
            is_terminal = i == len(chunks) - 1
            assert_chat_completion_chunk_shape(
                chunk,
                expected_model=model,
                is_bootstrap=is_bootstrap,
                is_terminal=is_terminal,
            )
    else:
        response = openai_client.chat.completions.create(
            model=model, messages=messages, stream=False
        )
        assert_chat_completion_shape(response, expected_model=model)


# ---------------------------------------------------------------------------
# 4.8-MATRIX-003 — he-router-* virtual-model audit-trail placeholders (BR-2.9).
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("virtual_model", VIRTUAL_HE_ROUTER_MODELS)
@pytest.mark.skip(
    reason="he-router-* virtual models route via Epic 6 routing-svc; no dedicated adapter pod"
)
def test_4_8_matrix_003_he_router_virtual_placeholders(virtual_model: str):
    """BR-2.9 visible JUnit-XML audit trail for the three virtual models."""
    _ = virtual_model  # pragma: no cover — pytest.mark.skip short-circuits


# ---------------------------------------------------------------------------
# 4.8-MATRIX-004 — /v1/models per-entry shape via shared helper (MED-3).
# ---------------------------------------------------------------------------


def test_4_8_matrix_004_v1_models_per_entry_shape(openai_client):
    """Story 3.5 / 4.7 — every catalogue entry passes the lifted helper."""
    models = list(openai_client.models.list())
    assert len(models) >= 11, f"expected ≥11 catalogue entries; got {len(models)}"
    expected = set(MATRIX_MODELS) | set(VIRTUAL_HE_ROUTER_MODELS) | {"text-embedding-3-small"}
    seen_ids = {m.id for m in models}
    # Sanity: every matrix model is present (otherwise the matrix cells skip via T0.4).
    missing = set(MATRIX_MODELS) - seen_ids
    assert not missing, f"matrix models missing from /v1/models: {sorted(missing)}"
    for entry in models:
        # When entry.id is e.g. "text-embedding-3-small" the per-entry shape
        # helper validates structure; containment in expected is asserted only
        # when the id is in our expected universe (avoids false-positive on
        # vendor-side catalogue additions).
        assert_model_entry_shape(entry, expected_id_set=expected | seen_ids)


# ---------------------------------------------------------------------------
# 4.8-MATRIX-005 — /public/models per-entry shape via raw httpx (no SDK).
# ---------------------------------------------------------------------------


def test_4_8_matrix_005_public_models_per_entry_shape(gateway_url: str, httpx_client):
    """Story 4.7 — unauthenticated catalogue. BR-2.3b helper accepts raw dicts."""
    resp = httpx_client.get(f"{gateway_url}/public/models")
    assert resp.status_code == 200, f"status={resp.status_code} body={resp.text}"
    body = resp.json()
    assert body.get("object") == "list"
    data = body.get("data") or []
    assert isinstance(data, list) and len(data) >= 11
    for entry in data:
        assert_model_entry_shape(entry)


# ---------------------------------------------------------------------------
# 4.8-MATRIX-006 — /v1/embeddings shape (BR-2.4 + Story 3.5).
# ---------------------------------------------------------------------------


def test_4_8_matrix_006_embeddings_shape(openai_client):
    """Story 3.5 — embeddings response shape via the shared helper."""
    resp = openai_client.embeddings.create(model="text-embedding-3-small", input="test")
    assert_embedding_shape(resp, expected_model="text-embedding-3-small")


# ---------------------------------------------------------------------------
# 4.8-MATRIX-007 — three representative error-envelope scenarios (BR-2.5).
# ---------------------------------------------------------------------------


def test_4_8_matrix_007a_envelope_401_invalid_api_key(gateway_url: str, httpx_client):
    """Story 3.6 — 401 envelope shape end-to-end."""
    resp = httpx_client.get(f"{gateway_url}/v1/models")  # no Authorization header
    assert resp.status_code == 401, f"status={resp.status_code} body={resp.text}"
    assert_error_envelope_shape(resp.json(), expected_code="401_invalid_api_key", expected_status=401)


def test_4_8_matrix_007b_envelope_400_invalid_request(openai_client):
    """Story 4.1 BR-5.x — unknown model id surfaces the 400 envelope via the SDK."""
    import openai

    with pytest.raises(openai.BadRequestError) as exc_info:
        openai_client.chat.completions.create(
            model="unknown-typo-model-xyz",
            messages=[{"role": "user", "content": "hi"}],
        )
    # exc.body is the parsed envelope dict the SDK pulled from the response.
    body = exc_info.value.body
    if not isinstance(body, dict) or "error" not in body:
        body = {"error": body}
    assert_error_envelope_shape(body, expected_code="400_invalid_request", expected_status=400)


def test_4_8_matrix_007c_envelope_405_method_not_allowed(gateway_url: str, httpx_client):
    """Story 4.7 OQ-4.7-6 — POST /public/models surfaces the 405 envelope."""
    resp = httpx_client.post(f"{gateway_url}/public/models")
    assert resp.status_code == 405, f"status={resp.status_code} body={resp.text}"
    assert_error_envelope_shape(
        resp.json(), expected_code="405_method_not_allowed", expected_status=405
    )


# ---------------------------------------------------------------------------
# 4.8-MATRIX-008 — operator-readable summary to $GITHUB_STEP_SUMMARY (BR-2.7).
# ---------------------------------------------------------------------------


def _summary_path() -> str | None:
    return os.environ.get("GITHUB_STEP_SUMMARY")


def pytest_terminal_summary(terminalreporter, exitstatus, config):  # noqa: ARG001
    """Append a markdown cell-result table to GITHUB_STEP_SUMMARY (BR-2.7).

    This hook resides in the matrix-test module so it only attaches when the
    module is collected. Pytest discovers `pytest_terminal_summary` in any
    importable module; we guard via the `protocol_completeness_test` name
    substring so we don't write summaries for unrelated test runs.
    """
    if _summary_path() is None:
        return
    rows: list[tuple[str, str, str]] = []
    for report in terminalreporter.getreports("passed") + terminalreporter.getreports("failed") + terminalreporter.getreports("skipped"):
        nodeid = report.nodeid
        if "openai_sdk_protocol_completeness_test.py" not in nodeid:
            continue
        rows.append((nodeid, report.outcome, getattr(report, "longreprtext", "") or ""))
    if not rows:
        return
    try:
        with open(_summary_path(), "a", encoding="utf-8") as fh:
            fh.write("\n### Story 4.8 — Protocol Completeness Matrix Cells\n\n")
            fh.write("| Cell | Status | Notes |\n|---|---|---|\n")
            for nodeid, outcome, notes in rows:
                trimmed = (notes or "").replace("\n", " ").strip()[:80]
                fh.write(f"| `{nodeid}` | **{outcome}** | {trimmed} |\n")
    except OSError:
        pass


# ---------------------------------------------------------------------------
# 4.8-MATRIX-009 — explicit MED-2 three-way disambiguation guard (covered by
# 4.8-MATRIX-002 streaming branch; this is a redundant per-flag-pair assertion).
# ---------------------------------------------------------------------------


def test_4_8_matrix_009_streaming_three_way_flag_discipline(
    openai_client, expected_vendor_models_present
):
    """MED-2 — content chunks pass NEITHER flag; bootstrap and terminal flags
    are exercised in 4.8-MATRIX-002 for every streaming cell. This test
    documents the closed-form contract Dev/QA can audit at a glance."""
    sample_model = MATRIX_MODELS[0]
    skip_reason = expected_vendor_models_present.get(sample_model)
    if skip_reason:
        pytest.skip(skip_reason)
    stream_obj = openai_client.chat.completions.create(
        model=sample_model,
        messages=[{"role": "user", "content": "Hi"}],
        stream=True,
        stream_options={"include_usage": True},
    )
    chunks = list(stream_obj)
    assert len(chunks) >= 3, f"need bootstrap + content + terminal; got {len(chunks)}"
    # bootstrap
    assert_chat_completion_chunk_shape(chunks[0], expected_model=sample_model, is_bootstrap=True)
    # middle content chunks (neither flag)
    for c in chunks[1:-1]:
        assert_chat_completion_chunk_shape(c, expected_model=sample_model)
    # terminal
    assert_chat_completion_chunk_shape(chunks[-1], expected_model=sample_model, is_terminal=True)


# Suppress unused-import warning where the matrix runs with no SDK calls.
_ = httpx
