"""Story 5.2 — E2E-001..005 (key-config + enforcement full-stack scenarios).

Tests in this module exercise the FULL console → gateway → auth-svc → PG →
Redis → Kafka loop for the Story-5.2 key-configuration + enforcement gates.
They require the same infra as the Story-5.1 E2E lane:

  - testcontainers-launched PG + Redis + Kafka
  - apps/auth-svc binary (with PG/Redis/Kafka env)
  - apps/api-gateway binary (with HE_API_AUTH_SVC_URL + HE_API_REDIS_URL +
    CLOUDFLARE_CIDRS / INGRESS_CIDRS for the trusted-proxy walker)
  - a signed-in he_access JWT cookie + a bearer API key issued via Story-5.1

In the default `pytest apps/api-gateway/tests/` lane these are SKIPPED with an
explicit reason. Unit-level + integration-level (in-process) coverage of the
same invariants is noted per scenario (Go packages: keypolicy, handlers,
middleware, apikey).

Source: T8.2 (story line 617-622) + design-doc §"Cross-cutting: E2E".
"""

import pytest


REQUIRES_E2E_INFRA = pytest.mark.skip(
    reason=(
        "E2E lane requires testcontainers (PG+Redis+Kafka) + running auth-svc + "
        "api-gateway + JWT cookie + bearer key helpers. Per-scenario invariants "
        "are covered by 5.2-UNIT-*/INT-* Go tests (keypolicy + handlers + "
        "middleware + apikey packages). CI process-mesh spawn is a tracked task."
    )
)


# Scenario: 5.2-E2E-001
@REQUIRES_E2E_INFRA
def test_5_2_e2e_001_configure_then_ip_whitelist_enforced():
    """PATCH ip_whitelist=["192.168.1.0/24"]; a bearer request from
    r.RemoteAddr=10.0.0.5 → 403 403_ip_not_whitelisted.

    UNIT/INT EQUIVALENT: keypolicy TestKeyPolicy_IPWhitelist (deny) +
    clientip TestResolveClientIP + handlers HandleUpdate INT-001.
    """


# Scenario: 5.2-E2E-002
@REQUIRES_E2E_INFRA
def test_5_2_e2e_002_configure_then_model_scope_enforced():
    """PATCH scope.models=["qwen-max"]; chat-completions model=deepseek-v3
    → 403 403_model_not_in_scope.

    UNIT/INT EQUIVALENT: keypolicy TestKeyPolicy_ModelScope (deny) +
    bodypeek TestPeekModel.
    """


# Scenario: 5.2-E2E-003
@REQUIRES_E2E_INFRA
def test_5_2_e2e_003_configure_then_monthly_cap_enforced():
    """PATCH monthly_cost_cap_usd="50.00"; pre-seed counter "50.01";
    chat-completions → 402 402_quota_exhausted.

    UNIT/INT EQUIVALENT: keypolicy TestKeyPolicy_MonthlyCap (over cap) +
    usage TestReadMonthlyCostUSD + checks TestCheckMonthlyCap.
    """


# Scenario: 5.2-E2E-004
@REQUIRES_E2E_INFRA
def test_5_2_e2e_004_configure_invalidates_cache_within_sla():
    """PATCH narrows scope; within ≤5s a bearer request bearing the
    just-narrowed-out model → 403 (config-updated sentinel purges the cache).

    UNIT/INT EQUIVALENT: middleware TestRequireAPIKey_ConfigUpdatedSentinelPurges.
    """


# Scenario: 5.2-E2E-005
@REQUIRES_E2E_INFRA
def test_5_2_e2e_005_idor_on_patch_collapses_to_404():
    """User A's session cookies PATCH user B's api_key_id → 404 collapse.

    UNIT/INT EQUIVALENT: apikey TestUpdateApiKey 5.2-UNIT-013 (cross-user
    NotFound) + handlers HandleUpdate INT-004.
    """
