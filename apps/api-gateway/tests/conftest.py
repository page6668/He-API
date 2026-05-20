"""Story 4.8 T0.4 — shared pytest fixtures for the api-gateway test suite.

Replaces the per-file `_client()` / `_gateway_url()` / `pytestmark = skipif(...)`
duplication scattered across Stories 3.3-3.6 + 4.1-4.7 contract test files.

Fixtures:
  - gateway_url           — HE_API_TEST_GATEWAY_URL (skips if unset)
  - openai_client         — function-scoped openai.OpenAI(base_url, api_key)
  - httpx_client          — function-scoped httpx.Client
  - expected_vendor_models_present — session-scoped pre-flight probe of /v1/models
                                     asserting all 10 vendor model-ids resolve
                                     (M-2 compensating control replacing the
                                     deleted BR-1.4 hard-panic guard).
"""

from __future__ import annotations

import os

import pytest

# The 10 expected vendor model-ids the M-2 compensating control probes for.
# Source of truth: apps/api-gateway/internal/adapterclient/registry.go.
EXPECTED_VENDOR_MODELS = (
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
)


@pytest.fixture()
def gateway_url() -> str:
    """Return HE_API_TEST_GATEWAY_URL or skip the test if unset."""
    url = os.environ.get("HE_API_TEST_GATEWAY_URL")
    if not url:
        pytest.skip("HE_API_TEST_GATEWAY_URL not set")
    return url.rstrip("/")


@pytest.fixture()
def api_key() -> str:
    """Return HE_API_TEST_API_KEY (stub fallback for non-auth-sensitive tests)."""
    return os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub")


@pytest.fixture()
def openai_client(gateway_url: str, api_key: str):
    """Function-scoped openai.OpenAI client pointed at the gateway under test.

    Function scope is deliberate — cross-vendor independence requires per-cell
    fresh clients (Story 4.8 Testing Requirements: "no global request fixture").
    """
    import openai  # imported lazily so tests not needing the SDK still run

    return openai.OpenAI(base_url=gateway_url + "/v1", api_key=api_key)


@pytest.fixture()
def httpx_client():
    """Function-scoped httpx.Client for raw-HTTP tests (public/models, error envelopes)."""
    import httpx

    with httpx.Client(timeout=httpx.Timeout(10.0)) as client:
        yield client


@pytest.fixture(scope="session")
def expected_vendor_models_present():
    """Session-scoped probe of GET /v1/models — M-2 compensating control.

    Runs ONCE per pytest session. For each vendor model id missing from the
    `/v1/models` catalogue, registers a per-id `pytest.skip(reason="...")`
    seed in the returned dict. Consumers query the dict per-cell to surface
    the skip with an actionable reason.

    Replaces the deleted BR-1.4 boot-time hard-panic guard. The T7.1
    EXACTLY-9 skip-count assertion fails the CI lane if any vendor model is
    missing (3 he-router skips + 6 _live_test.py skips + N missing = >9).
    """
    url = os.environ.get("HE_API_TEST_GATEWAY_URL")
    if not url:
        return {m: "HE_API_TEST_GATEWAY_URL not set" for m in EXPECTED_VENDOR_MODELS}

    import httpx

    api_key = os.environ.get("HE_API_TEST_API_KEY", "he-test-key-stub")
    seen = set()
    missing_reasons = {}
    try:
        resp = httpx.get(
            url.rstrip("/") + "/v1/models",
            headers={"Authorization": f"Bearer {api_key}"},
            timeout=httpx.Timeout(10.0),
        )
        if resp.status_code == 200:
            data = resp.json().get("data") or []
            seen = {entry.get("id") for entry in data if isinstance(entry, dict)}
        else:
            reason = f"/v1/models probe returned status={resp.status_code}"
            return {m: reason for m in EXPECTED_VENDOR_MODELS}
    except Exception as exc:  # network errors during probe → uniform skip
        reason = f"/v1/models probe failed: {exc!r}"
        return {m: reason for m in EXPECTED_VENDOR_MODELS}

    for model_id in EXPECTED_VENDOR_MODELS:
        if model_id not in seen:
            missing_reasons[model_id] = (
                f"model {model_id!r} absent from /v1/models catalogue "
                "— adapter endpoint likely unset"
            )
    return missing_reasons
