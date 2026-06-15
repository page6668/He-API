"""Story 10.2 — shared fixtures for the he-api SDK test suite.

Two jobs:
  1. Make the gateway's OpenAI-protocol shape-assertion library importable so the
     drop-in equivalence tests reuse the SAME oracle the gateway contract tests
     use (apps/api-gateway/tests/_protocol_invariants.py) — no shape-assertion
     copy-paste, single source of truth.
  2. Neutralise ambient credential/endpoint env vars per test, so key-resolution
     and base_url tests are deterministic regardless of the developer's shell.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

# --- (1) import the gateway protocol-invariants oracle -------------------------
# packages/sdk-python/tests/conftest.py -> repo root is three parents up.
_REPO_ROOT = Path(__file__).resolve().parents[3]
_GATEWAY_TESTS = _REPO_ROOT / "apps" / "api-gateway" / "tests"
if _GATEWAY_TESTS.is_dir() and str(_GATEWAY_TESTS) not in sys.path:
    sys.path.insert(0, str(_GATEWAY_TESTS))


_HE_ENV_VARS = ("HE_API_KEY", "HE_API_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL")


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch: pytest.MonkeyPatch):
    """Strip He/OpenAI credential + endpoint env vars before each test.

    Ensures default-base_url / key-isolation / precedence assertions are not
    contaminated by the ambient environment. Individual tests opt back in by
    setting the specific vars they exercise via ``monkeypatch.setenv``.
    """
    for var in _HE_ENV_VARS:
        monkeypatch.delenv(var, raising=False)
    yield
