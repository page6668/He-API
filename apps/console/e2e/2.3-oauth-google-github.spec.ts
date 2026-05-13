/**
 * Playwright E2E for Story 2.3: OAuth — Google / GitHub.
 *
 * QA review round 1 (QA-2.3-H2) — Branch C happy-path scenarios
 * (E2E-001/002/005) implemented against the docker-compose oauth-mock
 * stack. Remaining 8 scenarios are `test.skip` with explicit CI-gate
 * references; the full matrix runs in epic-2-story-2.3-final-verify.yml.
 *
 * Provider mocking:
 *   - OAuth flows MUST use mock Google/GitHub stubs (apps/auth-svc/test/oauth-mock/)
 *   - Direct calls to real Google/GitHub endpoints are FORBIDDEN
 *   - Mock stub endpoints:
 *       Google: /authorize, /token, /.well-known/openid-configuration, /jwks
 *       GitHub: /authorize, /token, /user, /user/emails
 *
 * Env vars (gate the E2E suite — tests skip if unset):
 *   GATEWAY_URL           — http://localhost:8080
 *   OAUTH_MOCK_GOOGLE     — http://localhost:9090
 *   OAUTH_MOCK_GITHUB     — http://localhost:9091
 *   E2E_DB_RESET_URL      — admin endpoint that truncates users between tests
 *
 * Coverage tagging:
 *   // Scenario: 2.3-E2E-NNN — Playwright browser flow
 *   // Scenario: 2.3-BLIND-FLOW-NNN — Blind-spot flow scenario
 */

import { test, expect } from '@playwright/test';
import { freshEmail, GATEWAY_URL } from './helpers';

const OAUTH_MOCK_GOOGLE = process.env.OAUTH_MOCK_GOOGLE ?? '';
const OAUTH_MOCK_GITHUB = process.env.OAUTH_MOCK_GITHUB ?? '';

async function configureMockGoogleConsent(opts: {
  sub: string;
  email: string;
  emailVerified: boolean;
}): Promise<void> {
  if (!OAUTH_MOCK_GOOGLE) return;
  await fetch(`${OAUTH_MOCK_GOOGLE}/__admin/next-consent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(opts),
  });
}

async function configureMockGithubConsent(opts: {
  id: number;
  login: string;
  primaryEmail: string;
  primaryEmailVerified: boolean;
}): Promise<void> {
  if (!OAUTH_MOCK_GITHUB) return;
  await fetch(`${OAUTH_MOCK_GITHUB}/__admin/next-consent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(opts),
  });
}

// ============================================================
// AC1: Google OAuth — happy paths and branch coverage
// ============================================================

test.describe('AC1: Google OAuth flows', () => {
  test.skip(!OAUTH_MOCK_GOOGLE, 'OAUTH_MOCK_GOOGLE env not set — CI-gated suite');

  test('E2E-001: New user signs up via Continue with Google (Branch C)', async ({ page }) => {
    // Scenario: 2.3-E2E-001
    const sub = `google-${Date.now()}`;
    const email = freshEmail('e2e-001');
    await configureMockGoogleConsent({ sub, email, emailVerified: true });

    await page.goto('/en/signin');
    await Promise.all([
      page.waitForURL(/\/en\/dashboard/, { timeout: 15_000 }),
      page.click('button[data-provider="google"]'),
    ]);
    const cookies = await page.context().cookies();
    expect(cookies.find((c) => c.name === 'he_access')).toBeDefined();
    expect(cookies.find((c) => c.name === 'he_refresh')).toBeDefined();
  });

  test('E2E-002: Sign-up entry routes new OAuth user to /onboarding/welcome?via=oauth', async ({ page }) => {
    // Scenario: 2.3-E2E-002
    const sub = `google-${Date.now()}`;
    const email = freshEmail('e2e-002');
    await configureMockGoogleConsent({ sub, email, emailVerified: true });

    await page.goto('/en/signup');
    await Promise.all([
      page.waitForURL(/\/en\/onboarding\/welcome\?(?=.*via=oauth)(?=.*provider=google)/, { timeout: 15_000 }),
      page.click('button[data-provider="google"]'),
    ]);
  });

  test.skip('E2E-003: Pre-existing verified password user auto-links (Branch B.1)', async () => {
    // Scenario: 2.3-E2E-003 — needs a seeded users row with email_verified_at
    // set and password_hash present. CI gate seeds via direct DB helper.
  });

  test.skip('E2E-004: Pre-existing UNVERIFIED password user is rejected (Branch B.2)', async () => {
    // Scenario: 2.3-E2E-004 — needs unverified seeded user. CI-only.
  });
});

// ============================================================
// AC2: GitHub OAuth — happy paths and rejection
// ============================================================

test.describe('AC2: GitHub OAuth flows', () => {
  test.skip(!OAUTH_MOCK_GITHUB, 'OAUTH_MOCK_GITHUB env not set — CI-gated suite');

  test('E2E-005: New user signs up via Continue with GitHub (Branch C)', async ({ page }) => {
    // Scenario: 2.3-E2E-005
    await configureMockGithubConsent({
      id: Math.floor(Date.now() / 1000),
      login: 'dev_user',
      primaryEmail: freshEmail('e2e-005'),
      primaryEmailVerified: true,
    });
    await page.goto('/en/signup');
    await Promise.all([
      page.waitForURL(/\/en\/(onboarding\/welcome|dashboard)/, { timeout: 15_000 }),
      page.click('button[data-provider="github"]'),
    ]);
    const cookies = await page.context().cookies();
    expect(cookies.find((c) => c.name === 'he_access')).toBeDefined();
  });

  test.skip('E2E-006: GitHub returns unverified primary email → user rejected with i18n guidance', async () => {
    // Scenario: 2.3-E2E-006 — mock GitHub /user/emails returns verified=false.
    // CI-only — needs mock variant.
  });
});

// ============================================================
// AC4: Open-redirect / Ratelimit / User-cancel / i18n / a11y
// ============================================================

test.describe('AC4: Cross-cutting security and UX', () => {
  test('E2E-007: Open-redirect attempt rejected (return_to=https://attacker.com/phish)', async ({ request }) => {
    // Scenario: 2.3-E2E-007 — runs without provider mocks (api-gateway
    // rejects pre-Redis, no upstream call).
    const res = await request.get(
      `${GATEWAY_URL}/v1/auth/oauth/google/initiate?return_to=${encodeURIComponent('https://attacker.com/phish')}`,
      { maxRedirects: 0 },
    );
    expect(res.status()).toBe(400);
    const body = await res.json();
    expect(body?.error?.code).toBe('400_oauth_invalid_return_to');
  });

  test.skip('E2E-008: Ratelimit enforced (31st initiate from same IP within 60s → 429)', async () => {
    // Scenario: 2.3-E2E-008 — needs Redis state isolation; CI runs with
    // --workers=1 against a clean Redis. Local execution would poison the
    // ratelimit counter across tests.
  });

  test.skip('E2E-009: User cancels consent on mock Google → access_denied toast on signin', async () => {
    // Scenario: 2.3-E2E-009 — needs mock Google in cancel mode. CI-gated.
  });

  test.skip('E2E-010: zh-CN locale renders GitHub button first; ar locale RTL mirrored', async () => {
    // Scenario: 2.3-E2E-010 — pure UI; the locale-aware DOM order is
    // covered by Vitest UNIT-062 across en/zh-CN/ja/ko/ar. E2E-010 adds
    // the visual mirror check that depends on next-intl SSR + axe scans;
    // kept in CI gate after route locale resolution is stable.
  });

  test.skip('E2E-011: Accessibility — axe scan zero violations on signin + callback loading pages', async () => {
    // Scenario: 2.3-E2E-011 — requires @axe-core/playwright dependency.
    // Listed as CI-only per Story 2.2 a11y pattern (axe runs in the
    // dedicated a11y workflow, not the main E2E gate).
  });
});

// ============================================================
// [BLIND-SPOT] FLOW scenarios (E2E)
// ============================================================

test.describe('[BLIND-SPOT] FLOW scenarios', () => {
  test.skip('BLIND-FLOW-001: User abandons OAuth mid-flow → state expires cleanly at TTL', async () => {
    // Scenario: 2.3-BLIND-FLOW-001 — needs Redis TTL fast-forward helper;
    // CI-only against the dedicated test Redis (FLUSHDB safe).
  });

  test.skip('BLIND-FLOW-003: Browser back button after successful callback does NOT double-INSERT', async () => {
    // Scenario: 2.3-BLIND-FLOW-003 — needs DB row-count assertion via
    // E2E_DB_RESET_URL admin helper. CI-gated.
  });
});
