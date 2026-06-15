import { test, expect } from '@playwright/test';

/**
 * Story 9.1b AC1+AC2 — /dashboard <UsageChart> E2E (9.1b-E2E-001).
 *
 * Authored as a test.skip stub (T4.3 / test-design caveat): the integrated
 * journey needs the full stack (console + api-gateway + auth-svc + ClickHouse)
 * plus a seeded authenticated session and seeded request_logs/hourly_agg rows —
 * the shared E2E-stack harness is not yet wired in the unit lane. The P0 IDOR
 * guarantee is therefore NOT left to E2E: it is fully covered at handler +
 * integration level by 9.1b-INT-005 (series_handler_test.go / series_integration_test.go).
 */

const SKIP = 'requires integrated stack (gateway+auth-svc+ClickHouse) + seeded session/usage';

test.describe('AC1+AC2: /dashboard usage-trend chart journey', () => {
  // 9.1b-E2E-001 (P1) — integrated journey: chart renders the series, toggle
  // switches group_by, empty state, IDOR (user A ≠ user B buckets), keyboard toggle.
  test.skip('9.1b-E2E-001: chart renders, toggle switches group_by, IDOR-fenced, keyboard-operable', async ({ page }) => {
    void SKIP;
    // Seed: user A with ≥1 day of multi-model usage; user B with heavy usage.
    // Login as A → /en/dashboard.
    await page.goto('/en/dashboard');

    // Chart renders the By Day trend beside the shipped stat-cards band.
    await expect(page.getByRole('heading', { name: 'Usage Trend' })).toBeVisible();
    const group = page.getByRole('radiogroup', { name: 'Group by' });
    await expect(group).toBeVisible();

    // Toggle By Model / By Status re-queries /series and re-renders.
    await page.getByRole('radio', { name: 'By Model' }).click();
    await page.getByRole('radio', { name: 'By Status' }).click();

    // Keyboard-only operation of the radiogroup.
    await page.getByRole('radio', { name: 'By Day' }).focus();
    await page.keyboard.press('ArrowRight');

    // IDOR: assert NO user-B bucket value ever appears in the chart's hidden
    // data table or in any /v1/me/usage/series network payload.
  });

  // 9.1b-E2E-001b — empty-history account → the chart EmptyState renders (no
  // broken Recharts / NaN axis), the stat-cards band still shows zeros.
  test.skip('9.1b-E2E-001b: empty history → chart EmptyState, dashboard intact', async ({ page }) => {
    void SKIP;
    await page.goto('/en/dashboard');
    await expect(page.getByText('No usage in the last 30 days.')).toBeVisible();
  });
});
