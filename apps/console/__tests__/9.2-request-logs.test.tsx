/**
 * Story 9.2: 实时调用日志（最近 1000 条） — Console log viewer (AC2) unit tests.
 *
 * Implements the QA test-design skeleton (Turing, 2026-06-11). The Go
 * gateway/store scenarios (9.2-UNIT-001..016 / INT-001..008) are TDD-driven in
 * apps/api-gateway/internal/analyticsquery/*_test.go. The E2E scenarios
 * (9.2-E2E-001..004) live in apps/console/e2e/9.2-logs.spec.ts.
 *
 * Test Design: docs/qa/assessments/9.2-test-design-20260611.md
 */

import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

import { RequestLogsTable } from '@/components/business/RequestLogsTable';
import { RequestLogsTableSkeleton } from '@/components/business/RequestLogsTableSkeleton';
import { RequestLogsFilters } from '@/components/business/RequestLogsFilters';
import { RequestLogsPagination } from '@/components/business/RequestLogsPagination';
import { RequestLogsError } from '@/components/business/RequestLogsError';
import { ConsoleSidebarNav } from '@/components/ConsoleSidebarNav';
import { UsageLogsPageSchema, type UsageLogsPage } from '@/lib/api/me-usage';
import { signinReturnPath, logsRetryHref } from '@/app/[locale]/(console)/logs/_lib/paths';
import enLogs from '@/messages/en/logs.json';

// ---- module mocks ----
const routerMock = { push: vi.fn(), replace: vi.fn(), refresh: vi.fn() };
let currentSearch = new URLSearchParams();
vi.mock('next/navigation', () => ({
  useRouter: () => routerMock,
  usePathname: () => '/en/logs',
  useSearchParams: () => currentSearch,
}));

beforeEach(() => {
  routerMock.push.mockReset();
  currentSearch = new URLSearchParams();
});

function renderIntl(ui: React.ReactNode, locale = 'en') {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ logs: enLogs }}>
      {ui}
    </NextIntlClientProvider>,
  );
}

const fixturePage: UsageLogsPage = {
  items: [
    {
      he_request_id: 'req_aaaaaaaaaaaa',
      ts: '2026-06-11T10:00:00Z',
      model: 'qwen-max',
      upstream_model: 'qwen-max',
      status_code: 200,
      is_streaming: true,
      prompt_tokens: 10000,
      completion_tokens: 35200,
      total_tokens: 45200,
      latency_ms_total: 1234,
      ttfb_ms: 120,
      api_key_id: '22222222-2222-2222-2222-222222222222',
      error_code: '',
    },
    {
      he_request_id: 'req_bbbbbbbbbbbb',
      ts: '2026-06-11T09:59:00Z',
      model: 'qwen-plus',
      upstream_model: 'qwen-plus',
      status_code: 503,
      is_streaming: false,
      prompt_tokens: 5,
      completion_tokens: 0,
      total_tokens: 5,
      latency_ms_total: 80,
      ttfb_ms: 0,
      api_key_id: '33333333-3333-3333-3333-333333333333',
      error_code: '503_clickhouse_unavailable',
    },
  ],
  total_count: 2,
  limit: 50,
  offset: 0,
  has_more: false,
};

// ============================================================
// AC2: 控制台调用日志页 — filterable, paginated log table
// ============================================================

describe('AC2: RequestLogsTable — render & columns', () => {
  test('9.2-UNIT-017: renders 8 columns from a fixture page; status as label+icon; numbers via Intl', () => {
    renderIntl(<RequestLogsTable page={fixturePage} locale="en" resetHref="/en/logs" />);

    // Semantic <table> with exactly the 8 BR-UI columns.
    const headers = screen.getAllByRole('columnheader');
    expect(headers).toHaveLength(8);
    for (const label of ['Time', 'Model', 'Status', 'Streaming', 'Tokens', 'Latency', 'API Key', 'Request ID']) {
      expect(screen.getByRole('columnheader', { name: label })).toBeInTheDocument();
    }

    // Status conveyed by text + icon (NOT colour alone — WCAG 1.4.1).
    expect(screen.getByText('Success')).toBeInTheDocument();
    expect(screen.getByText('Server error')).toBeInTheDocument();
    expect(screen.getByText('✓')).toBeInTheDocument();
    expect(screen.getByText('✕')).toBeInTheDocument();

    // Counts/latency via Intl.NumberFormat (en → comma groups).
    expect(screen.getByText('45,200')).toBeInTheDocument(); // total tokens row 1
    expect(screen.getByText(/1,234\s*ms/)).toBeInTheDocument(); // latency row 1

    // Request IDs render (LTR code cells).
    expect(screen.getByText('req_aaaaaaaaaaaa')).toBeInTheDocument();
  });

  test('9.2-UNIT-018: no cost/消费 column present (BR-UI-4)', () => {
    renderIntl(<RequestLogsTable page={fixturePage} locale="en" resetHref="/en/logs" />);
    const headerNames = screen.getAllByRole('columnheader').map((h) => h.textContent ?? '');
    for (const name of headerNames) {
      expect(name.toLowerCase()).not.toMatch(/cost|spend|消费/);
    }
    // Exactly 8 columns — no 9th (cost) column.
    expect(headerNames).toHaveLength(8);
  });
});

describe('AC2: RequestLogsTable — states', () => {
  test('9.2-UNIT-019: [BLIND-SPOT] loading skeleton; empty (total_count=0) → EmptyState + reset CTA', () => {
    // Loading skeleton announces via role="status".
    const { unmount } = renderIntl(<RequestLogsTableSkeleton label="Loading request logs…" />);
    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-label', 'Loading request logs…');
    expect(status).toHaveAttribute('aria-busy', 'true');
    unmount();

    // Empty result → EmptyState with a reset-filters CTA (never NaN).
    const emptyPage: UsageLogsPage = { items: [], total_count: 0, limit: 50, offset: 0, has_more: false };
    renderIntl(<RequestLogsTable page={emptyPage} locale="en" resetHref="/en/logs" />);
    expect(screen.getByTestId('request-logs-empty')).toBeInTheDocument();
    expect(screen.getByText('No requests match these filters')).toBeInTheDocument();
    const resetCta = screen.getByText('Reset filters');
    expect(resetCta).toHaveAttribute('href', '/en/logs');
    // No table rendered when empty.
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  test('9.2-UNIT-020: [BLIND-SPOT] 503 → inline retry (filters retained); 401 → redirect /signin?return_to=/logs', () => {
    // 503 → inline RequestLogsError; the Retry link preserves the current query
    // (filters retained — the filter bar lives above this banner on the page).
    renderIntl(
      <RequestLogsError
        message={enLogs.errors.unavailable}
        retryLabel={enLogs.errors.retry}
        retryHref={logsRetryHref('en', 'status=server_error&model=qwen-max')}
      />,
    );
    const banner = screen.getByTestId('request-logs-error');
    expect(within(banner).getByText(enLogs.errors.unavailable)).toBeInTheDocument();
    const retry = within(banner).getByText('Retry');
    expect(retry).toHaveAttribute('href', '/en/logs?status=server_error&model=qwen-max');

    // 401 → the page bounces to /signin with a return_to back to /logs.
    expect(signinReturnPath('en')).toBe('/en/signin?return_to=%2Fen%2Flogs');
  });

  test('9.2-UNIT-021: [BLIND-SPOT] malformed payload fails UsageLogsPageSchema.safeParse → generic error, no crash', () => {
    // The Zod safety net: a shape-drifted payload parses to !success (BR-UI-2) so
    // the action returns logs.errors.malformed rather than the component crashing.
    expect(UsageLogsPageSchema.safeParse({ items: 'not-an-array', total_count: 'x' }).success).toBe(false);
    expect(UsageLogsPageSchema.safeParse(null).success).toBe(false);
    // A well-formed page still passes.
    expect(UsageLogsPageSchema.safeParse(fixturePage).success).toBe(true);

    // Rendering the generic error state never throws.
    expect(() =>
      renderIntl(
        <RequestLogsError message={enLogs.errors.malformed} retryLabel={enLogs.errors.retry} retryHref="/en/logs" />,
      ),
    ).not.toThrow();
  });
});

describe('AC2: RequestLogsFilters — filter bar & pagination', () => {
  test('9.2-UNIT-022: filter validation — end>start, both RFC3339; invalid → inline error, query NOT fired', () => {
    renderIntl(<RequestLogsFilters />);

    // end <= start → inline error, NO navigation.
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-06-11T10:00' } });
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-06-11T09:00' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    expect(screen.getByRole('alert')).toHaveTextContent('End time must be after start time.');
    expect(routerMock.push).not.toHaveBeenCalled();

    // Fix the range → query IS fired.
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-06-11T11:00' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    expect(routerMock.push).toHaveBeenCalledTimes(1);
  });

  test('9.2-UNIT-023: [BLIND-SPOT] pagination — Prev/Next honor has_more; filter change resets offset 0; URL reflects state', () => {
    // --- pagination honours has_more ---
    currentSearch = new URLSearchParams('status=server_error');
    const { unmount } = renderIntl(<RequestLogsPagination offset={0} limit={50} hasMore />);
    const prev = screen.getByRole('button', { name: 'Previous' });
    const next = screen.getByRole('button', { name: 'Next' });
    expect(prev).toBeDisabled(); // offset 0
    expect(next).toBeEnabled(); // has_more
    fireEvent.click(next);
    // Next preserves the filter and advances offset; URL reflects both (BR-UI-3).
    expect(routerMock.push).toHaveBeenCalledWith(expect.stringContaining('status=server_error'));
    expect(routerMock.push).toHaveBeenCalledWith(expect.stringContaining('offset=50'));
    unmount();

    // Next disabled when has_more=false.
    renderIntl(<RequestLogsPagination offset={50} limit={50} hasMore={false} />);
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled();

    // --- a filter change resets offset to 0 (omitted from the pushed URL) ---
    routerMock.push.mockReset();
    currentSearch = new URLSearchParams('offset=50&status=server_error');
    renderIntl(<RequestLogsFilters />);
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'qwen-max' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    expect(routerMock.push).toHaveBeenCalledTimes(1);
    const pushed = routerMock.push.mock.calls[0][0] as string;
    expect(pushed).toContain('model=qwen-max');
    expect(pushed).not.toContain('offset'); // reset to 0 → omitted
  });

  test('9.2-UNIT-024: sidebar nav toggles aria-current on active /logs route (BR-UI-8)', () => {
    // usePathname is mocked to /en/logs.
    render(
      <ConsoleSidebarNav
        items={[
          { href: '/en/dashboard', label: 'Dashboard' },
          { href: '/en/logs', label: 'Request Logs' },
          { href: '/en/keys', label: 'API Keys' },
        ]}
      />,
    );
    const active = screen.getByRole('link', { name: 'Request Logs' });
    expect(active).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Dashboard' })).not.toHaveAttribute('aria-current');
  });
});

describe('AC2: get-usage-logs Server Action & i18n', () => {
  test('9.2-INT-009: action — use server, cookie passthrough, no-store, discriminated union ok/unauthorized/error', async () => {
    vi.resetModules();
    vi.doMock('next/headers', () => ({
      cookies: () => ({ getAll: () => [{ name: 'he_access', value: 'tok-123' }] }),
    }));
    const { getUsageLogs } = await import('@/app/[locale]/(console)/logs/_actions/get-usage-logs');

    // 200 → ok:true; fetch must forward the cookie + cache:'no-store'.
    const fetchMock = vi.fn(async () => ({
      status: 200,
      json: async () => fixturePage,
    })) as unknown as typeof fetch;
    vi.stubGlobal('fetch', fetchMock);

    const ok = await getUsageLogs({ status: 'server_error', limit: 50 });
    expect(ok.ok).toBe(true);
    const call = (fetchMock as unknown as ReturnType<typeof vi.fn>).mock.calls[0]!;
    const url = call[0] as string;
    const init = call[1] as { cache?: string; headers?: Record<string, string> };
    expect(String(url)).toContain('/v1/me/usage/logs');
    expect(String(url)).toContain('status=server_error');
    expect(init.cache).toBe('no-store');
    expect(init.headers?.Cookie).toContain('he_access=tok-123');

    // 401 → unauthorized sentinel.
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 401, json: async () => ({}) })) as unknown as typeof fetch);
    const unauth = await getUsageLogs();
    expect(unauth.ok).toBe(false);
    if (!unauth.ok) expect(unauth.unauthorized).toBe(true);

    // 503 envelope → unavailable error code.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ status: 503, json: async () => ({ error: { code: '503_clickhouse_unavailable' } }) })) as unknown as typeof fetch,
    );
    const down = await getUsageLogs();
    expect(down.ok).toBe(false);
    if (!down.ok) expect(down.error.code).toBe('logs.errors.unavailable');

    vi.unstubAllGlobals();
    vi.doUnmock('next/headers');
    vi.resetModules();
  });

  test('9.2-INT-010: i18n key-completeness — logs.json ×10 key parity', async () => {
    const locales = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'zh-CN'];
    function keys(obj: Record<string, unknown>, prefix = ''): string[] {
      let out: string[] = [];
      for (const k of Object.keys(obj)) {
        const kk = prefix ? `${prefix}.${k}` : k;
        out.push(kk);
        const v = obj[k];
        if (v && typeof v === 'object') out = out.concat(keys(v as Record<string, unknown>, kk));
      }
      return out.sort();
    }
    const enKeys = keys(enLogs as unknown as Record<string, unknown>);
    for (const loc of locales) {
      const mod = (await import(`@/messages/${loc}/logs.json`)).default;
      expect(keys(mod), `${loc}/logs.json key parity`).toEqual(enKeys);
    }
  });
});
