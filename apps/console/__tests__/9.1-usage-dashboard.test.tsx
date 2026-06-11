/**
 * Story 9.1: 用量大盘（实时 + 历史聚合） — Dashboard cards (AC3) unit tests.
 *
 * Implements the QA test-design skeleton (Turing) for the AC3 frontend
 * scenarios. The Go producer/consumer/reader scenarios (9.1-UNIT-001..012,
 * 9.1-INT-*) are TDD-driven in co-located *_test.go. The E2E scenarios
 * (9.1-E2E-001..005, 9.1-VIS-001) live in apps/console/e2e/9.1-dashboard.spec.ts.
 * NO chart tests — AC4 (UsageChart / /series) is DEFERRED to 9.1b (Architect H-4).
 *
 * Test Design: docs/qa/assessments/9.1-test-design-20260611.md
 */

import { describe, test, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

import { UsageStatCards } from '@/components/business/UsageStatCards';
import { UsageStatCardsSkeleton } from '@/components/business/UsageStatCardsSkeleton';
import { UsageSummarySchema, type UsageSummary } from '@/lib/api/me-usage';
import enMessages from '@/messages/en/dashboard.json';

function renderCards(summary: UsageSummary, locale = 'en') {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ dashboard: enMessages }}>
      <UsageStatCards summary={summary} locale={locale} />
    </NextIntlClientProvider>,
  );
}

const fullSummary: UsageSummary = {
  today: { requests: 1234, success_rate: '0.9724', tokens: { prompt: 10000, completion: 35200, total: 45200 }, cost_usd: '0.42' },
  month: { requests: 24890, success_rate: '0.9810', tokens: { prompt: 1, completion: 2, total: 900000 }, cost_usd: '12.50' },
  quarter: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
};

const emptySummary: UsageSummary = {
  today: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
  month: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
  quarter: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
};

describe('AC3: UsageStatCards — 3×4 metric matrix', () => {
  // 9.1-UNIT-013
  test('renders 3 periods × 4 metrics; null→"—", counts grouped, cost via money helper', () => {
    renderCards(fullSummary);

    expect(screen.getByRole('heading', { name: 'Today' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'This Month' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'This Quarter' })).toBeInTheDocument();

    // Counts grouped via Intl.NumberFormat (en → comma groups).
    expect(screen.getByText('1,234')).toBeInTheDocument();
    expect(screen.getByText('45,200')).toBeInTheDocument(); // today total tokens

    // success_rate as a locale percent (2dp).
    expect(screen.getByText('97.24%')).toBeInTheDocument();

    // quarter has requests=0 → success_rate "—" (never NaN).
    const quarterCard = screen.getByRole('heading', { name: 'This Quarter' }).closest('section')!;
    expect(within(quarterCard).getByText('—')).toBeInTheDocument();

    // 消费 via the Story-5.5 money helper (formatDecimal → "$0.42").
    expect(screen.getByText('$0.42')).toBeInTheDocument();
    expect(screen.getByText('$12.50')).toBeInTheDocument();
  });

  // R2-4 — 消费 degrades independently: cost_usd null renders "—" while the
  // other three cards (请求数/成功率/Token) stay live (usage_ledger unavailable).
  test('cost_usd null → "—"; the other three metrics still render (R2-4)', () => {
    const partialSummary: UsageSummary = {
      today: { requests: 1234, success_rate: '0.9724', tokens: { prompt: 10000, completion: 35200, total: 45200 }, cost_usd: null },
      month: { requests: 24890, success_rate: '0.9810', tokens: { prompt: 1, completion: 2, total: 900000 }, cost_usd: null },
      quarter: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: null },
    };
    renderCards(partialSummary);

    // The today card: 请求数/成功率/Token live, 消费 "—".
    const todayCard = screen.getByRole('heading', { name: 'Today' }).closest('section')!;
    expect(within(todayCard).getByText('1,234')).toBeInTheDocument();
    expect(within(todayCard).getByText('97.24%')).toBeInTheDocument();
    expect(within(todayCard).getByText('45,200')).toBeInTheDocument();
    expect(within(todayCard).getByText('—')).toBeInTheDocument(); // 消费 cell only
    // Never a currency symbol when cost is unavailable.
    expect(within(todayCard).queryByText(/\$/)).toBeNull();
    expect(UsageSummarySchema.safeParse(partialSummary).success).toBe(true);
  });

  // [BLIND-SPOT] 9.1-UNIT-014 (BOUNDARY-001)
  test('empty state — 0 / "—" / 0 / $0.00 + first-call hint; never NaN', () => {
    renderCards(emptySummary);

    expect(screen.getAllByText('0').length).toBeGreaterThanOrEqual(3);
    expect(screen.getAllByText('—').length).toBe(3);
    expect(screen.getAllByText('$0.00').length).toBe(3);

    expect(screen.getByTestId('usage-empty-hint')).toHaveTextContent(enMessages.empty.hint);
    expect(screen.queryByText(/NaN|Infinity/)).toBeNull();
  });

  // 9.1-UNIT-015 (loading half; error+retry is page-level → 9.1-E2E-005)
  test('loading skeleton renders with aria-live polite (one-shot)', () => {
    render(
      <NextIntlClientProvider locale="en" messages={{ dashboard: enMessages }}>
        <UsageStatCardsSkeleton label={enMessages.loading.label} />
      </NextIntlClientProvider>,
    );
    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-busy', 'true');
    expect(status).toHaveAttribute('aria-live', 'polite');
    expect(status).toHaveAccessibleName(enMessages.loading.label);
    // (The ErrorBanner + Retry Server-Action form is the page path — covered by
    // the 9.1-E2E-005 retry journey, not unit-testable without the RSC boundary.)
  });

  // 9.1-UNIT-016
  test('Zod safety-net — malformed summary fails UsageSummarySchema.safeParse', () => {
    const malformed = {
      today: { requests: 'oops', success_rate: 0.97, tokens: {}, cost_usd: 1 },
    };
    expect(UsageSummarySchema.safeParse(malformed).success).toBe(false);
    expect(UsageSummarySchema.safeParse(fullSummary).success).toBe(true);
  });

  // 9.1-UNIT-017
  test('responsive — ≥md 3 period columns; <md stacked (grid-cols-1 / md:grid-cols-3)', () => {
    const { container } = renderCards(fullSummary);
    const grid = container.querySelector('.grid')!;
    expect(grid.className).toContain('grid-cols-1');
    expect(grid.className).toContain('md:grid-cols-3');
  });

  // 9.1-UNIT-018 — sidebar nav aria-current="page" on /dashboard.
  test('sidebar nav toggles aria-current="page" on the active /dashboard route', async () => {
    vi.resetModules();
    vi.doMock('next/navigation', () => ({ usePathname: () => '/en/dashboard' }));
    const { ConsoleSidebarNav } = await import('@/components/ConsoleSidebarNav');
    render(
      <ConsoleSidebarNav
        items={[
          { href: '/en/dashboard', label: 'Dashboard' },
          { href: '/en/keys', label: 'API Keys' },
        ]}
      />,
    );
    expect(screen.getByRole('link', { name: 'Dashboard' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'API Keys' })).not.toHaveAttribute('aria-current');
    vi.doUnmock('next/navigation');
  });
});
