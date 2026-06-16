/**
 * Story 6.5 (AC1) — RoutingStrategyForm unit tests (Vitest + Testing Library).
 *
 * Implements the QA test-design skeleton (Turing, 2026-06-16). Pattern mirrors
 * apps/console/components/business/ProfileForm.test.tsx (Story 2.5).
 * Test Design: docs/qa/assessments/6.5-test-design-20260616.md
 */

import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NextIntlClientProvider, type AbstractIntlMessages } from 'next-intl';

import { RoutingStrategyForm } from './RoutingStrategyForm';
import enMessages from '@/messages/en/account.json';

// 10-locale account messages (i18n gate — 6.5-UNIT-008).
import zhCN from '@/messages/zh-CN/account.json';
import ja from '@/messages/ja/account.json';
import ko from '@/messages/ko/account.json';
import es from '@/messages/es/account.json';
import fr from '@/messages/fr/account.json';
import de from '@/messages/de/account.json';
import pt from '@/messages/pt/account.json';
import ru from '@/messages/ru/account.json';
import ar from '@/messages/ar/account.json';

type ActionInput = { value: string | null; ifMatch: string; currentLocale: string };

// Mock the Server Action so no HTTP fires in unit tests.
const updateMock = vi.fn((_input: ActionInput): Promise<unknown> =>
  Promise.resolve({ kind: 'ok', etag: '"new"' }),
);
vi.mock(
  '@/app/[locale]/(console)/settings/profile/_actions/update-my-routing-strategy',
  () => ({ updateMyRoutingStrategy: (input: ActionInput) => updateMock(input) }),
);

beforeEach(() => {
  updateMock.mockClear();
});

function renderForm(
  persisted: string | null,
  messages: AbstractIntlMessages = enMessages as AbstractIntlMessages,
) {
  return render(
    <NextIntlClientProvider locale="en" messages={{ account: messages }}>
      <RoutingStrategyForm defaultRoutingStrategy={persisted} etag={'"123"'} currentLocale="en" />
    </NextIntlClientProvider>,
  );
}

// radios in DOM order: [0] passthrough, [1] quality, [2] cost, [3] latency.
function radio(i: number): HTMLInputElement {
  const r = screen.getAllByRole('radio')[i];
  if (!r) throw new Error(`no radio at index ${i}`);
  return r as HTMLInputElement;
}
function saveBtn(): HTMLButtonElement {
  return screen.getByRole('button', { name: enMessages.routing.actions.save }) as HTMLButtonElement;
}

describe('AC1: RoutingStrategyForm — select + persist', () => {
  test('6.5-UNIT-001: renders the 4 choices and pre-selects the persisted value', () => {
    renderForm('cost');
    expect(screen.getAllByRole('radio')).toHaveLength(4);
    expect(radio(2).checked).toBe(true); // cost pre-selected
    expect(radio(0).checked).toBe(false);
  });

  test('6.5-UNIT-002: value mapping — passthrough→null, quality/cost/latency→string', async () => {
    const user = userEvent.setup();
    const cases: Array<[string | null, number, string | null]> = [
      ['cost', 0, null], // passthrough clears
      ['cost', 1, 'quality'],
      [null, 2, 'cost'],
      ['cost', 3, 'latency'],
    ];
    for (const [persisted, idx, expected] of cases) {
      updateMock.mockClear();
      const { unmount } = renderForm(persisted);
      await user.click(radio(idx));
      await user.click(saveBtn());
      await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
      expect(updateMock.mock.calls[0]?.[0]).toMatchObject({ value: expected });
      unmount();
    }
  });

  test('6.5-UNIT-003: dirty detection — Save disabled until selection differs', async () => {
    const user = userEvent.setup();
    renderForm('cost');
    expect(saveBtn()).toBeDisabled(); // equal to persisted
    await user.click(radio(2)); // re-select cost (still equal)
    expect(saveBtn()).toBeDisabled();
    await user.click(radio(1)); // quality — now differs
    expect(saveBtn()).toBeEnabled();
  });

  test('6.5-UNIT-004: save success → success banner; selection retained', async () => {
    const user = userEvent.setup();
    renderForm('cost');
    await user.click(radio(1)); // quality
    await user.click(saveBtn());
    expect(await screen.findByText(enMessages.routing.toast.saved)).toBeInTheDocument();
    expect(radio(1).checked).toBe(true);
  });

  test('6.5-UNIT-005: save error → error banner; no optimistic mutation', async () => {
    updateMock.mockResolvedValueOnce({ kind: 'error' });
    const user = userEvent.setup();
    renderForm('cost');
    await user.click(radio(1)); // quality
    await user.click(saveBtn());
    expect(await screen.findByText(enMessages.routing.errors.generic)).toBeInTheDocument();
    // No success banner shown (no false-positive persist).
    expect(screen.queryByText(enMessages.routing.toast.saved)).not.toBeInTheDocument();
  });

  test('6.5-UNIT-006: all visible strings resolve via NextIntlClientProvider (no hard-coded copy)', () => {
    // A sentinel message set proves the component reads from i18n, not literals.
    const sentinel: AbstractIntlMessages = {
      routing: {
        title: '__T_TITLE__',
        description: '__T_DESC__',
        options: { passthrough: '__O_PT__', quality: '__O_Q__', cost: '__O_C__', latency: '__O_L__' },
        hints: { passthrough: '__H_PT__', quality: '__H_Q__', cost: '__H_C__', latency: '__H_L__' },
        actions: { save: '__SAVE__' },
        toast: { saved: '__SAVED__' },
        errors: { generic: '__ERR__', rate_limited: '__RL__' },
        banners: { concurrent_update: { message: '__CC__', reload_cta: '__RELOAD__' } },
      },
    };
    renderForm('cost', sentinel);
    expect(screen.getByText('__T_TITLE__')).toBeInTheDocument();
    expect(screen.getByText('__O_C__')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '__SAVE__' })).toBeInTheDocument();
  });

  test('6.5-UNIT-007: pending state disables the control during save', async () => {
    let resolveFn: ((v: unknown) => void) | undefined;
    updateMock.mockImplementationOnce(() => new Promise<unknown>((r) => { resolveFn = r; }));
    const user = userEvent.setup();
    renderForm('cost');
    await user.click(radio(1));
    await user.click(saveBtn());
    await waitFor(() => expect(radio(1)).toBeDisabled());
    expect(saveBtn()).toBeDisabled();
    resolveFn?.({ kind: 'ok', etag: '"x"' });
  });

  test('[BLIND-SPOT] 6.5-BLIND-FLOW-001: double-click Save issues exactly one call', async () => {
    let resolveFn: ((v: unknown) => void) | undefined;
    updateMock.mockImplementationOnce(() => new Promise<unknown>((r) => { resolveFn = r; }));
    const user = userEvent.setup();
    renderForm('cost');
    await user.click(radio(1));
    const btn = saveBtn();
    await user.click(btn);
    await user.click(btn); // second click while pending → guarded
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    resolveFn?.({ kind: 'ok', etag: '"x"' });
  });
});

describe('AC1 i18n: account namespace keys', () => {
  test('6.5-UNIT-008: routing keys present in all 10 locales', () => {
    const locales: Record<string, { routing?: Record<string, unknown> }> = {
      en: enMessages, 'zh-CN': zhCN, ja, ko, es, fr, de, pt, ru, ar,
    };
    for (const [loc, msgs] of Object.entries(locales)) {
      const r = msgs.routing;
      expect(r, `${loc} missing routing block`).toBeTruthy();
      expect(r?.title, `${loc} routing.title`).toBeTruthy();
      const opts = r?.options as Record<string, string>;
      for (const k of ['passthrough', 'quality', 'cost', 'latency']) {
        expect(opts[k], `${loc} routing.options.${k}`).toBeTruthy();
      }
      expect((r?.actions as Record<string, string>).save, `${loc} routing.actions.save`).toBeTruthy();
      expect((r?.toast as Record<string, string>).saved, `${loc} routing.toast.saved`).toBeTruthy();
    }
  });
});
