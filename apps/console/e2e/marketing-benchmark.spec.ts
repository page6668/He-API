/**
 * Story 10.6 — public `(marketing)/benchmark` Playwright E2E (AC2).
 *
 * The page is PUBLIC + server-rendered from the committed curated seed, so the
 * suite is self-contained (no gateway / no auth). Scenarios:
 *
 *   10.6-E2E-006        anon /en/benchmark → 3 metric views + methodology +
 *                       last-updated + disclaimer + "Open A/B in Playground"; SEO meta
 *   10.6-E2E-007 (P2)   /ar/benchmark RTL — layout flips, numbers/model-ids/units
 *                       stay LTR islands (not mirrored)
 *   10.6-INT-023        3 metric bars render (Recharts)
 *   10.6-INT-024        public no-login + SEO generateMetadata (title/desc/og/canonical)
 *   10.6-INT-026 (P2)   vendor filter narrows the displayed set
 *   10.6-INT-027 (P2)   chart a11y: radiogroup + hidden table (9.1b precedent)
 */
import { test, expect } from '@playwright/test';

test.describe('AC2: public benchmark page', () => {
  test('10.6-E2E-006 / INT-023 / INT-024: anon page renders metrics + provenance + SEO', async ({ page }) => {
    await page.goto('/en/benchmark');

    // INT-023 — Recharts bars container present + the 3 metric radios.
    await expect(page.getByTestId('benchmark-bars')).toBeVisible();
    await expect(page.getByTestId('benchmark-metric-quality')).toBeVisible();
    await expect(page.getByTestId('benchmark-metric-cost')).toBeVisible();
    await expect(page.getByTestId('benchmark-metric-latency')).toBeVisible();

    // Provenance (BR-10.6.9): methodology + last-updated + disclaimer.
    await expect(page.getByTestId('benchmark-methodology')).toBeVisible();
    await expect(page.getByTestId('benchmark-disclaimer')).toBeVisible();

    // "Open A/B in Playground" jump → console playground.
    const openAB = page.getByTestId('benchmark-open-ab');
    await expect(openAB).toBeVisible();
    await expect(openAB).toHaveAttribute('href', '/en/playground');

    // INT-024 — SEO meta in <head> (BR-10.6.12).
    await expect(page).toHaveTitle(/Benchmark/i);
    await expect(page.locator('head link[rel="canonical"]')).toHaveAttribute('href', 'https://he-api.com/en/benchmark');
    await expect(page.locator('head meta[property="og:title"]')).toHaveCount(1);
  });

  test('10.6-INT-027: chart a11y — metric radiogroup + screen-reader data table', async ({ page }) => {
    await page.goto('/en/benchmark');
    const group = page.getByRole('radiogroup');
    await expect(group).toBeVisible();
    // hidden full-metric table mirror for AT.
    await expect(page.getByTestId('benchmark-table')).toHaveCount(1);
    // keyboard: focus the checked radio, ArrowRight switches metric.
    await page.getByTestId('benchmark-metric-quality').focus();
    await page.keyboard.press('ArrowRight');
    await expect(page.getByTestId('benchmark-metric-cost')).toHaveAttribute('aria-checked', 'true');
  });

  test('10.6-INT-026: vendor filter narrows the displayed set', async ({ page }) => {
    await page.goto('/en/benchmark');
    const table = page.getByTestId('benchmark-table');
    const allRows = await table.locator('tbody tr').count();
    await page.getByTestId('benchmark-filter').selectOption('he');
    const heRows = await table.locator('tbody tr').count();
    expect(heRows).toBeLessThan(allRows);
    expect(heRows).toBeGreaterThan(0);
  });

  test('10.6-E2E-007: /ar/benchmark RTL — numbers / model ids / units stay LTR islands', async ({ page }) => {
    await page.goto('/ar/benchmark');
    // document is RTL…
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    // …but the Recharts SVG container is a forced LTR island (x-axis not mirrored)…
    await expect(page.getByTestId('benchmark-bars')).toHaveAttribute('dir', 'ltr');
    // …and the data cells (model id / number / unit) are LTR islands.
    const firstId = page.getByTestId('benchmark-table').locator('tbody tr').first().locator('th');
    await expect(firstId.locator('[dir="ltr"]')).toHaveCount(1);
  });
});
