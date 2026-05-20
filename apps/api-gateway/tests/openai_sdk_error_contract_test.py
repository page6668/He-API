"""SDK contract test for Story 3.6 AC1 + AC2 (OpenAI-compatible error envelope + X-He-Request-Id header).

Test Design: docs/qa/assessments/3.6-test-design-20260519.md

Environment contract (inherited from Story 3.3 / 3.4 / 3.5):
  - HE_API_TEST_GATEWAY_URL — gateway base URL (e.g., http://localhost:8080)
  - HE_API_TEST_API_KEY — valid bearer token for VALID-bearer scenarios
  - Tests with bad-bearer scenarios construct a client with api_key="bad-key-xxxxx"
  - openai==1.40.* + pytest==8.* + re for the request-id regex assertion
"""

import os
import re

import pytest

# Story 4.8 T4.10 — rewire to shared invariants library + reuse REQUEST_ID_RE
# canonical constant so envelope-regex drift surfaces in one place.
from _protocol_invariants import REQUEST_ID_RE as _SHARED_REQUEST_ID_RE
from _protocol_invariants import assert_error_envelope_shape

try:
    import openai
except ImportError:  # pragma: no cover — SDK is optional in local dev
    openai = None


REQUEST_ID_RE = _SHARED_REQUEST_ID_RE  # re-export for backwards compat


def _gateway_url() -> str:
    url = os.environ.get("HE_API_TEST_GATEWAY_URL")
    if not url:
        pytest.skip("HE_API_TEST_GATEWAY_URL not set — skipping SDK contract test")
    return url


def _valid_api_key() -> str:
    key = os.environ.get("HE_API_TEST_API_KEY")
    if not key:
        pytest.skip("HE_API_TEST_API_KEY not set — skipping SDK contract test")
    return key


def _require_sdk():
    if openai is None:
        pytest.skip("openai SDK not installed — skipping SDK contract test")


def _assert_envelope(exc, *, expected_code: str, expected_status: int,
                     expected_type: str = "invalid_request_error"):
    """Common assertions on an openai.APIError subclass.

    Story 4.8 T4.10 — delegates the body-shape clauses to the shared
    `assert_error_envelope_shape` helper. The SDK exception's `body`
    attribute carries the parsed full envelope; when only the inner
    `error` dict is exposed we wrap it back into the canonical envelope.
    """
    body = getattr(exc, "body", None)
    if isinstance(body, dict) and "error" not in body:
        # SDK normalises some shapes to only the inner error dict.
        body = {"error": body}
    if isinstance(body, dict):
        assert_error_envelope_shape(body, expected_code=expected_code, expected_status=expected_status)
    # SDK attribute lens (preserved from Story 3.6 — independent of body shape).
    assert getattr(exc, "code", None) == expected_code, (
        f"exc.code: got={getattr(exc, 'code', None)!r} want={expected_code!r}"
    )
    assert getattr(exc, "type", None) == expected_type, (
        f"exc.type: got={getattr(exc, 'type', None)!r} want={expected_type!r}"
    )
    assert getattr(exc, "param", None) is None, (
        f"exc.param: got={getattr(exc, 'param', None)!r} want=None"
    )
    rid = getattr(exc, "request_id", "") or ""
    assert REQUEST_ID_RE.match(rid), (
        f"exc.request_id (from X-He-Request-Id header): got={rid!r} want match {REQUEST_ID_RE.pattern}"
    )


# ============================================================
# AC1 + AC2 — SDK Contract (E2E, P0)
# ============================================================


def test_sdk_invalid_bearer_chat_completions_raises_authentication_error():
    """3.6-E2E-001 (P0): invalid bearer → openai.AuthenticationError."""
    _require_sdk()
    url = _gateway_url()
    client = openai.OpenAI(api_key="bad-key-xxxxx", base_url=url + "/v1")
    with pytest.raises(openai.AuthenticationError) as exc_info:
        client.chat.completions.create(
            model="qwen-max",
            messages=[{"role": "user", "content": "hi"}],
        )
    _assert_envelope(exc_info.value, expected_code="401_invalid_api_key", expected_status=401)


def test_sdk_oversized_body_raises_api_error_413():
    """3.6-E2E-002 (P0): 2 MiB body → openai.APIError + 413 envelope."""
    _require_sdk()
    url = _gateway_url()
    key = _valid_api_key()
    client = openai.OpenAI(api_key=key, base_url=url + "/v1")
    huge = "a" * 2_000_000
    with pytest.raises(openai.APIError) as exc_info:
        client.chat.completions.create(
            model="qwen-max",
            messages=[{"role": "user", "content": huge}],
        )
    _assert_envelope(exc_info.value, expected_code="413_payload_too_large", expected_status=413)


def test_sdk_invalid_bearer_models_list_raises_authentication_error():
    """3.6-E2E-003 (P0): invalid bearer on /v1/models → same envelope shape."""
    _require_sdk()
    url = _gateway_url()
    client = openai.OpenAI(api_key="bad-key-xxxxx", base_url=url + "/v1")
    with pytest.raises(openai.AuthenticationError) as exc_info:
        client.models.list()
    _assert_envelope(exc_info.value, expected_code="401_invalid_api_key", expected_status=401)


def test_sdk_invalid_bearer_embeddings_raises_authentication_error():
    """3.6-E2E-004 (P0): invalid bearer on /v1/embeddings → same envelope shape."""
    _require_sdk()
    url = _gateway_url()
    client = openai.OpenAI(api_key="bad-key-xxxxx", base_url=url + "/v1")
    with pytest.raises(openai.AuthenticationError) as exc_info:
        client.embeddings.create(model="text-embedding-3-small", input="hi")
    _assert_envelope(exc_info.value, expected_code="401_invalid_api_key", expected_status=401)


def test_sdk_success_path_carries_x_he_request_id_header():
    """3.6-E2E-005 (P0): valid bearer success path — response header matches regex."""
    _require_sdk()
    url = _gateway_url()
    key = _valid_api_key()
    client = openai.OpenAI(api_key=key, base_url=url + "/v1")
    # Use with_raw_response so we can read the underlying HTTP response headers.
    raw = client.chat.completions.with_raw_response.create(
        model="qwen-max",
        messages=[{"role": "user", "content": "hi"}],
    )
    header = raw.headers.get("x-he-request-id") or raw.headers.get("X-He-Request-Id")
    assert header, "X-He-Request-Id header missing on success response"
    assert REQUEST_ID_RE.match(header), (
        f"X-He-Request-Id={header!r} does not match {REQUEST_ID_RE.pattern}"
    )
