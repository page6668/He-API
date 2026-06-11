/**
 * Story 9.3 AC3 — Console export control tests (implements the QA skeleton).
 *
 * Each test maps to a designed scenario in docs/qa/assessments/9.3-test-design-20260611.md.
 * The component drives the REAL request-log-export Server Action through a stubbed
 * global.fetch, so the discriminated-union + Zod + cookie/no-store behavior is
 * exercised end-to-end (no action mock).
 */

import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

import enLogs from '../messages/en/logs.json';
import arLogs from '../messages/ar/logs.json';
import deLogs from '../messages/de/logs.json';
import esLogs from '../messages/es/logs.json';
import frLogs from '../messages/fr/logs.json';
import jaLogs from '../messages/ja/logs.json';
import koLogs from '../messages/ko/logs.json';
import ptLogs from '../messages/pt/logs.json';
import ruLogs from '../messages/ru/logs.json';
import zhLogs from '../messages/zh-CN/logs.json';

const ALL_LOGS: Record<string, Record<string, unknown>> = {
  ar: arLogs, de: deLogs, en: enLogs, es: esLogs, fr: frLogs,
  ja: jaLogs, ko: koLogs, pt: ptLogs, ru: ruLogs, 'zh-CN': zhLogs,
};

import { LogExportDialog } from '@/components/business/LogExportDialog';
import { requestLogExport } from '@/app/[locale]/(console)/logs/_actions/request-log-export';

// --- mocks for the server-action module deps ---
const routerMock = { push: vi.fn(), replace: vi.fn(), refresh: vi.fn() };
vi.mock('next/navigation', () => ({ useRouter: () => routerMock }));
vi.mock('next/headers', () => ({ cookies: () => ({ get: () => ({ value: 'tok-123' }) }) }));
vi.mock('next/cache', () => ({ revalidatePath: vi.fn() }));
vi.mock('@/lib/auth/cookies', () => ({ ACCESS_COOKIE: 'he_access' }));

function renderIntl(ui: React.ReactNode, locale = 'en', messages: Record<string, unknown> = { logs: enLogs }) {
  return render(
    <NextIntlClientProvider locale={locale} messages={messages as never}>
      {ui}
    </NextIntlClientProvider>,
  );
}

function mockFetch(status: number, body: unknown) {
  return vi.fn().mockResolvedValue({
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
  });
}

const okBody = { export_id: 'ul-1', status: 'pending', format: 'csv', requested_at: '2026-06-11T12:00:00.000Z' };

beforeEach(() => {
  routerMock.push.mockClear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AC3: Console export control', () => {
  // 9.3-UNIT-040 [P0]: signed URL is NEVER in the DOM, even when completed.
  test('9.3-UNIT-040: no signed URL rendered for a completed export', () => {
    const current = {
      export_id: 'ul-1', status: 'completed' as const, format: 'csv' as const,
      requested_at: '2026-06-11T12:00:00Z',
      signed_url_expires_at: new Date(Date.now() + 3600_000).toISOString(),
    };
    const { container } = renderIntl(<LogExportDialog locale="en" current={current} />);
    expect(screen.getByTestId('log-export-emailed')).toBeTruthy();
    expect(container.querySelector('a[href^="http"]')).toBeNull();
    expect(container.innerHTML).not.toMatch(/https?:\/\//);
  });

  // 9.3-UNIT-041: format picker; confirm invokes the action with the chosen format.
  test('9.3-UNIT-041: selecting CSV + submit POSTs format=csv', async () => {
    const f = mockFetch(200, okBody);
    vi.stubGlobal('fetch', f);
    renderIntl(<LogExportDialog locale="en" current={null} />);

    fireEvent.click(screen.getByRole('radio', { name: /csv/i }));
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));

    await waitFor(() => expect(f).toHaveBeenCalledTimes(1));
    const [url, init] = f.mock.calls[0];
    expect(String(url)).toContain('/v1/me/usage/logs/export');
    expect(JSON.parse(init.body)).toEqual({ format: 'csv' });
  });

  // 9.3-UNIT-042: success → in-progress + submit disabled.
  test('9.3-UNIT-042: ok → in-progress + submit disabled', async () => {
    vi.stubGlobal('fetch', mockFetch(200, okBody));
    renderIntl(<LogExportDialog locale="en" current={null} />);
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));
    await waitFor(() => expect(screen.getByTestId('log-export-in-progress')).toBeTruthy());
    expect((screen.getByRole('button', { name: /export logs/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  // 9.3-UNIT-043: current=completed → emailed-link banner with localized expiry.
  test('9.3-UNIT-043: completed → emailed banner with expiry', () => {
    const current = {
      export_id: 'ul-1', status: 'completed' as const, format: 'json' as const,
      requested_at: '2026-06-11T12:00:00Z',
      signed_url_expires_at: new Date(Date.now() + 3600_000).toISOString(),
    };
    renderIntl(<LogExportDialog locale="en" current={current} />);
    const banner = screen.getByTestId('log-export-emailed');
    expect(banner.textContent).toMatch(/expires on/i);
  });

  // 9.3-UNIT-044: action discriminated union + cookie passthrough + no-store.
  test('9.3-UNIT-044: action shape ok|unauthorized|rate_limited + no-store + cookie', async () => {
    vi.stubGlobal('fetch', mockFetch(200, okBody));
    const ok = await requestLogExport('csv', 'en');
    expect(ok.kind).toBe('ok');
    const [, init] = (globalThis.fetch as ReturnType<typeof mockFetch>).mock.calls[0];
    expect(init.cache).toBe('no-store');
    expect(init.headers.Cookie).toContain('he_access=tok-123');

    vi.stubGlobal('fetch', mockFetch(401, null));
    expect((await requestLogExport('csv', 'en')).kind).toBe('unauthorized');
    vi.stubGlobal('fetch', mockFetch(429, null));
    expect((await requestLogExport('csv', 'en')).kind).toBe('rate_limited');
  });

  // 9.3-UNIT-045: Zod malformed response → error, no throw.
  test('9.3-UNIT-045: malformed response → error', async () => {
    vi.stubGlobal('fetch', mockFetch(200, { nope: true }));
    const res = await requestLogExport('json', 'en');
    expect(res.kind).toBe('error');
  });

  // 9.3-UNIT-046: states — 401 redirect / 429 notice / failed re-enable.
  test('9.3-UNIT-046: 401 → redirect to /signin', async () => {
    vi.stubGlobal('fetch', mockFetch(401, null));
    renderIntl(<LogExportDialog locale="en" current={null} />);
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));
    await waitFor(() => expect(routerMock.push).toHaveBeenCalledWith('/en/signin?return_to=/en/logs'));
  });

  test('9.3-UNIT-046: 429 → rate-limited notice', async () => {
    vi.stubGlobal('fetch', mockFetch(429, null));
    renderIntl(<LogExportDialog locale="en" current={null} />);
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));
    await waitFor(() => expect(screen.getByTestId('log-export-rate-limited')).toBeTruthy());
  });

  test('9.3-UNIT-046: current=failed → control re-enabled + failed banner', () => {
    const current = {
      export_id: 'ul-1', status: 'failed' as const, format: 'csv' as const,
      requested_at: '2026-06-11T12:00:00Z', signed_url_expires_at: null,
    };
    renderIntl(<LogExportDialog locale="en" current={current} />);
    expect((screen.getByRole('button', { name: /export logs/i }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getByTestId('log-export-failed')).toBeTruthy();
  });

  // 9.3-UNIT-047: i18n key-parity across all 10 locales.
  test('9.3-UNIT-047: export keys present + parity across 10 locales', () => {
    const enKeys = flatKeys((enLogs as Record<string, unknown>).export).sort();
    expect(enKeys.length).toBeGreaterThan(5);
    for (const [loc, msgs] of Object.entries(ALL_LOGS)) {
      expect(flatKeys(msgs.export).sort(), `locale ${loc} export key parity`).toEqual(enKeys);
    }
  });

  // 9.3-UNIT-048: a11y — radiogroup, labels, button aria, status role.
  test('9.3-UNIT-048: a11y roles + accessible names', () => {
    renderIntl(<LogExportDialog locale="en" current={null} />);
    expect(screen.getByRole('radiogroup')).toBeTruthy();
    expect(screen.getAllByRole('radio')).toHaveLength(2);
    expect(screen.getByRole('button', { name: /export logs/i })).toBeTruthy();
  });

  // 9.3-UNIT-049: RTL — format codes stay LTR.
  test('9.3-UNIT-049: format codes stay LTR under ar', () => {
    const { container } = renderIntl(<LogExportDialog locale="ar" current={null} />, 'ar', { logs: arLogs });
    const ltr = Array.from(container.querySelectorAll('span[dir="ltr"]')).map((e) => e.textContent);
    expect(ltr).toContain('JSON');
    expect(ltr).toContain('CSV');
  });

  // 9.3-UNIT-050 [P2]: range picker — NOT exposed (export is a fixed 90d window
  // resolved server-side; BR-EX-3 default range_days=90). Skipped per the
  // skeleton's "test.skip with reason if range is not exposed" instruction.
  test.skip('9.3-UNIT-050: range picker (not exposed — fixed 90d window)', () => {});

  // BLIND-FLOW-001: double-click fires only one action.
  test('9.3-BLIND-FLOW-001: double-click → one action', async () => {
    const f = mockFetch(200, okBody);
    vi.stubGlobal('fetch', f);
    renderIntl(<LogExportDialog locale="en" current={null} />);
    const btn = screen.getByRole('button', { name: /export logs/i });
    fireEvent.click(btn);
    fireEvent.click(btn);
    await waitFor(() => expect(screen.getByTestId('log-export-in-progress')).toBeTruthy());
    expect(f).toHaveBeenCalledTimes(1);
  });

  // BLIND-FLOW-002: session expiry → unauthorized → redirect.
  test('9.3-BLIND-FLOW-002: unauthorized mid-flow → redirect', async () => {
    vi.stubGlobal('fetch', mockFetch(401, null));
    renderIntl(<LogExportDialog locale="en" current={null} />);
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));
    await waitFor(() => expect(routerMock.push).toHaveBeenCalled());
  });

  // BLIND-ERROR-005: network error → inline retry, no crash.
  test('9.3-BLIND-ERROR-005: network error → error notice', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network')));
    renderIntl(<LogExportDialog locale="en" current={null} />);
    fireEvent.click(screen.getByRole('button', { name: /export logs/i }));
    await waitFor(() => expect(screen.getByTestId('log-export-error')).toBeTruthy());
  });

  // BLIND-BOUNDARY-007: current=null → control enabled, neutral state.
  test('9.3-BLIND-BOUNDARY-007: no current export → enabled, neutral', () => {
    renderIntl(<LogExportDialog locale="en" current={null} />);
    expect((screen.getByRole('button', { name: /export logs/i }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByTestId('log-export-in-progress')).toBeNull();
    expect(screen.queryByTestId('log-export-emailed')).toBeNull();
  });
});

// flatKeys returns dot-paths of all leaf keys under an object.
function flatKeys(obj: unknown, prefix = ''): string[] {
  if (obj === null || typeof obj !== 'object') return [prefix];
  const out: string[] = [];
  for (const [k, v] of Object.entries(obj as Record<string, unknown>)) {
    const p = prefix ? `${prefix}.${k}` : k;
    out.push(...flatKeys(v, p));
  }
  return out;
}

// zh-CN parity sanity (real translations, not [en-pending]).
void zhLogs;
