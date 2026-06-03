/**
 * Story 5.5 — Vitest unit + integration suite (implements the QA skeleton).
 *
 * AUTO-GENERATED skeleton (Turing, 2026-06-03) implemented by Dev (Linus).
 * Every designed scenario ID is preserved. Bodies are written against the
 * SHIPPED contract, applying the Architect/QA corrections:
 *   - schemas are KeyEntrySchema / ListKeysResponseSchema (envelope
 *     {object:"list", data:[...]}), NOT the SM's pre-correction
 *     ApiKeyEntrySchema / {api_keys:[...]} (5.5-UNIT-003).
 *   - Q-E5 OVERRULE: no cap_tripped field; CapBudgetBar heuristic-computes
 *     current >= cap (5.5-UNIT-002 / 008 / BLIND-DATA-002 / GAP-CAP-001).
 *
 * E2E / Security(E) / A11y(E) / Visual scenarios live in the Playwright specs
 * (apps/console/e2e/5.5-keys-page{,-a11y}.spec.ts).
 */

import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, within, waitFor, cleanup, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NextIntlClientProvider } from 'next-intl';

import enAccount from '@/messages/en/account.json';
import {
  KeyEntrySchema,
  ListKeysResponseSchema,
  KeyNameSchema,
  PLAINTEXT_RE,
  type KeyEntry,
} from '@/lib/api/me-keys';
import { parseDecimal, formatDecimal, toCanonicalString } from '@/lib/api/money';
import { validateIpRule } from '@/lib/api/ip';
import { CapBudgetBar } from '@/components/business/CapBudgetBar';
import { ScopeChips } from '@/components/business/ScopeChips';
import { KeysTable } from '@/components/business/KeysTable';
import { KeysTableSkeleton } from '@/components/business/KeysTableSkeleton';
import { KeysEmptyState } from '@/components/business/KeysEmptyState';
import { KeysPanel } from '@/components/business/KeysPanel';
import { CreateKeyModal } from '@/components/business/CreateKeyModal';
import { ApiKeyDisplay } from '@/components/business/ApiKeyDisplay';
import { ConfigureKeyDrawer } from '@/components/business/ConfigureKeyDrawer';
import { IpWhitelistEditor } from '@/components/business/IpWhitelistEditor';
import { RevokeKeyDialog } from '@/components/business/RevokeKeyDialog';
import { Dialog } from '@/components/ui/dialog';

// ---- module mocks ----
const routerMock = { push: vi.fn(), replace: vi.fn(), refresh: vi.fn() };
vi.mock('next/navigation', () => ({
  useRouter: () => routerMock,
  usePathname: () => '/en/keys',
}));
vi.mock('next/headers', () => ({
  cookies: () => {
    throw new Error('no request scope');
  },
}));
vi.mock('@/app/[locale]/(console)/keys/_actions/create-key', () => ({ createMyKey: vi.fn() }));
vi.mock('@/app/[locale]/(console)/keys/_actions/update-key', () => ({ updateMyKey: vi.fn() }));
vi.mock('@/app/[locale]/(console)/keys/_actions/revoke-key', () => ({ revokeMyKey: vi.fn() }));

import { createMyKey } from '@/app/[locale]/(console)/keys/_actions/create-key';
import { updateMyKey } from '@/app/[locale]/(console)/keys/_actions/update-key';
import { revokeMyKey } from '@/app/[locale]/(console)/keys/_actions/revoke-key';
import { listMyKeys } from '@/app/[locale]/(console)/keys/_actions/list-keys';

const createMyKeyMock = vi.mocked(createMyKey);
const updateMyKeyMock = vi.mocked(updateMyKey);
const revokeMyKeyMock = vi.mocked(revokeMyKey);

// ---- helpers ----
function renderIntl(ui: React.ReactNode, locale = 'en') {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ account: enAccount }}>
      {ui}
    </NextIntlClientProvider>,
  );
}

function makeRow(overrides: Partial<KeyEntry> = {}): KeyEntry {
  return {
    api_key_id: '11111111-1111-4111-8111-111111111111',
    name: 'Prod key',
    key_prefix: 'he-ABCDEF123',
    scope: { models: [], ip_whitelist: [] },
    monthly_cost_cap_usd: '50.00',
    current_month_cost_usd: '10.00',
    last_used_at: null,
    revoked_at: null,
    created_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

const CANONICAL_PLAINTEXT = `he-${'A'.repeat(43)}`;

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(() => cleanup());

// ============================================================
// AC1: List — me-keys Zod schemas
// ============================================================

describe('AC1: List — me-keys Zod schemas', () => {
  test('5.5-UNIT-001: KeyEntrySchema parses a valid wire row (string-decimal money, lowercase uuid, RFC3339)', () => {
    expect(KeyEntrySchema.safeParse(makeRow()).success).toBe(true);
  });
  test('5.5-UNIT-002: KeyEntrySchema has NO required cap_tripped field (Q-E5 overrule) — row without it parses', () => {
    const parsed = KeyEntrySchema.parse(makeRow());
    expect('cap_tripped' in parsed).toBe(false);
  });
  test('5.5-UNIT-003: ListKeysResponseSchema parses {object:"list", data:[...]} envelope (corrected from {api_keys:[...]})', () => {
    expect(ListKeysResponseSchema.safeParse({ object: 'list', data: [makeRow()] }).success).toBe(true);
    expect(ListKeysResponseSchema.safeParse([makeRow()]).success).toBe(false); // bare array rejected
    expect(ListKeysResponseSchema.safeParse({ api_keys: [makeRow()] }).success).toBe(false); // wrong envelope
  });
  test('5.5-UNIT-004: KeyEntrySchema rejects malformed monthly_cost_cap_usd ("50" / number)', () => {
    expect(KeyEntrySchema.safeParse(makeRow({ monthly_cost_cap_usd: '50' })).success).toBe(false);
    expect(KeyEntrySchema.safeParse(makeRow({ monthly_cost_cap_usd: 50 as unknown as string })).success).toBe(false);
  });
});

// ============================================================
// AC1: CapBudgetBar heuristic (client-side per Q-E5)
// ============================================================

describe('AC1: List — CapBudgetBar heuristic (client-side per Q-E5)', () => {
  test('5.5-UNIT-005: cap=null → "No cap set", gray, no bar', () => {
    const { container } = renderIntl(<CapBudgetBar current="10.00" cap={null} locale="en" />);
    expect(screen.getByText('No cap set')).toBeInTheDocument();
    expect(container.querySelector('[role="img"]')).toBeNull();
  });
  test('5.5-UNIT-006: current/cap < 0.80 → green token', () => {
    const { container } = renderIntl(<CapBudgetBar current="40.00" cap="100.00" locale="en" />);
    expect(container.querySelector('.bg-green-500')).not.toBeNull();
  });
  test('5.5-UNIT-007: 0.80 <= current/cap < 1.0 → amber token', () => {
    const { container } = renderIntl(<CapBudgetBar current="80.00" cap="100.00" locale="en" />);
    expect(container.querySelector('.bg-amber-500')).not.toBeNull();
  });
  test('5.5-UNIT-008: current >= cap → red token + "Tripped" badge (heuristic)', () => {
    const { container } = renderIntl(<CapBudgetBar current="100.00" cap="100.00" locale="en" />);
    expect(container.querySelector('.bg-red-500')).not.toBeNull();
    expect(screen.getByText('Tripped')).toBeInTheDocument();
  });
  test('5.5-UNIT-009: formats current/cap via formatDecimal(locale,"USD") for en/de/ar', () => {
    for (const loc of ['en', 'de', 'ar']) {
      const { container, unmount } = renderIntl(<CapBudgetBar current="100.00" cap="200.00" locale={loc} />);
      expect(container.textContent).toContain(formatDecimal('100.00', loc, 'USD'));
      unmount();
    }
  });
});

// ============================================================
// AC1: KeysTable / EmptyState (component)
// ============================================================

describe('AC1: List — KeysTable / EmptyState (component)', () => {
  function renderTable(keys: KeyEntry[]) {
    const onConfigure = vi.fn();
    const onRevoke = vi.fn();
    const result = renderIntl(
      <KeysTable keys={keys} locale="en" onConfigure={onConfigure} onRevoke={onRevoke} />,
    );
    return { ...result, onConfigure, onRevoke };
  }

  test('5.5-INT-001: desktop renders all 8 columns from KeyEntry[]', () => {
    const { container } = renderTable([makeRow()]);
    const table = container.querySelector('table')!;
    for (const h of ['Name', 'Prefix', 'Scope', 'Created', 'Last used', 'Monthly cap', 'Status', 'Actions']) {
      expect(within(table).getByText(h)).toBeInTheDocument();
    }
  });
  test('5.5-INT-002: mobile (<md) renders <article role="region"> card stack', () => {
    renderTable([makeRow(), makeRow({ api_key_id: '22222222-2222-4222-8222-222222222222' })]);
    expect(screen.getAllByRole('region', { name: /API key/ })).toHaveLength(2);
  });
  test('5.5-INT-003: revoked row → opacity-50 + "Revoked" badge + NO action buttons', () => {
    const { container } = renderTable([makeRow({ revoked_at: '2026-06-02T00:00:00Z' })]);
    expect(container.querySelector('tr.opacity-50')).not.toBeNull();
    expect(screen.getAllByText('Revoked').length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: /Configure/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Revoke Prod key/ })).toBeNull();
  });
  test('5.5-INT-004: last_used=null → renders account.keys.last_used.never', () => {
    renderTable([makeRow({ last_used_at: null })]);
    expect(screen.getAllByText('Never').length).toBeGreaterThan(0);
  });
  test('5.5-INT-005: ScopeChips — empty models → "All models" chip; non-empty → per-model chips + "+N IPs"', () => {
    const { unmount } = renderIntl(<ScopeChips scope={{ models: [], ip_whitelist: [] }} />);
    expect(screen.getByText('All models')).toBeInTheDocument();
    unmount();
    renderIntl(<ScopeChips scope={{ models: ['qwen-max', 'deepseek-v3'], ip_whitelist: ['1.2.3.4'] }} />);
    expect(screen.getByText('qwen-max')).toBeInTheDocument();
    expect(screen.getByText('deepseek-v3')).toBeInTheDocument();
    expect(screen.getByText('+1 IPs')).toBeInTheDocument();
  });
  test('5.5-INT-006: KeysTableSkeleton renders 3 rows, aria-busy + one-shot aria-live', () => {
    const { container } = render(<KeysTableSkeleton label="Loading your API keys" />);
    const region = screen.getByRole('status');
    expect(region).toHaveAttribute('aria-busy', 'true');
    expect(region).toHaveAttribute('aria-label', 'Loading your API keys');
    expect(container.querySelectorAll('.animate-pulse').length).toBeGreaterThanOrEqual(3);
  });
  test('5.5-INT-007: EmptyState renders heading/body/CTA + Key icon; CTA opens CreateKeyModal', async () => {
    const onCreate = vi.fn();
    renderIntl(<KeysEmptyState onCreate={onCreate} />);
    expect(screen.getByText('No API keys yet')).toBeInTheDocument();
    expect(screen.getByText(/Create your first API key to start/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /Create your first API key/ }));
    expect(onCreate).toHaveBeenCalledOnce();
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-009: >100 keys → first 100 only, footer count, no "Load more" (BR-L-7)', () => {
    const keys = Array.from({ length: 100 }, (_, i) =>
      makeRow({ api_key_id: `${(i + 10).toString(16).padStart(8, '0')}-1111-4111-8111-111111111111` }),
    );
    renderIntl(<KeysPanel keys={keys} locale="en" availableModels={[]} />);
    expect(screen.getByText('Showing 100 keys')).toBeInTheDocument();
    expect(screen.queryByText(/Load more/i)).toBeNull();
  });
});

// ============================================================
// AC2: Create — name validation
// ============================================================

describe('AC2: Create — name validation (BR-C-1, mirrors Story 5.1 BR-1.7)', () => {
  test('5.5-UNIT-010: accepts valid NFC name "Production API"', () => {
    expect(KeyNameSchema.safeParse('Production API').success).toBe(true);
  });
  test('5.5-UNIT-011: 1-char name accepted (min boundary)', () => {
    expect(KeyNameSchema.safeParse('X').success).toBe(true);
  });
  test('5.5-UNIT-012: 100 runes accepted; 101 runes rejected (rune count not bytes)', () => {
    expect(KeyNameSchema.safeParse('a'.repeat(100)).success).toBe(true);
    expect(KeyNameSchema.safeParse('a'.repeat(101)).success).toBe(false);
  });
  test('5.5-UNIT-013: empty/whitespace-only rejected → too_short/missing', () => {
    expect(KeyNameSchema.safeParse('').success).toBe(false);
    expect(KeyNameSchema.safeParse('   ').success).toBe(false);
  });
  test('5.5-UNIT-014: applies normalize("NFC") transform', () => {
    const decomposed = 'café'; // e + combining acute
    const parsed = KeyNameSchema.parse(decomposed);
    expect(parsed).toBe('café'.normalize('NFC'));
    expect(parsed.normalize('NFC')).toBe(parsed);
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-008: surrogate / private-use / control / emoji code-point rejected', () => {
    expect(KeyNameSchema.safeParse('Party \u{1F389} key').success).toBe(false); // emoji (So)
    expect(KeyNameSchema.safeParse('badbell').success).toBe(false); // control
    expect(KeyNameSchema.safeParse('priv\u{E000}use').success).toBe(false); // private-use
  });
});

describe('AC2: Create — plaintext wire/regex guards', () => {
  test('5.5-UNIT-015: plaintext guard matches he-<base62> (42–43); rejects malformed', () => {
    expect(PLAINTEXT_RE.test(CANONICAL_PLAINTEXT)).toBe(true);
    expect(PLAINTEXT_RE.test(`he-${'A'.repeat(42)}`)).toBe(true);
    expect(PLAINTEXT_RE.test('he-short')).toBe(false);
    expect(PLAINTEXT_RE.test(`he-${'!'.repeat(43)}`)).toBe(false);
  });
  test('5.5-UNIT-016: sub-page plaintext-shape validator rejects "junk", accepts canonical (BR-PD-4)', () => {
    expect(PLAINTEXT_RE.test('junk')).toBe(false);
    expect(PLAINTEXT_RE.test('')).toBe(false);
    expect(PLAINTEXT_RE.test(CANONICAL_PLAINTEXT)).toBe(true);
  });
});

// ============================================================
// AC2: CreateKeyModal (component)
// ============================================================

describe('AC2: Create — CreateKeyModal (component)', () => {
  test('5.5-INT-008: Save disabled until valid; enables on valid input (BR-C-2)', async () => {
    renderIntl(<CreateKeyModal locale="en" onClose={vi.fn()} />);
    const save = screen.getByRole('button', { name: 'Create key' });
    expect(save).toBeDisabled();
    await userEvent.type(screen.getByLabelText('Key name'), 'Production API');
    expect(save).toBeEnabled();
  });
  test('5.5-INT-009: ESC / Cancel close; focus returns to [+ New Key] (Q-FOCUS1)', async () => {
    const onClose = vi.fn();
    renderIntl(<CreateKeyModal locale="en" onClose={onClose} />);
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalled();
    onClose.mockClear();
    await userEvent.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalled();
  });
  test('[BLIND-SPOT] 5.5-A11Y-008: focus restoration to trigger after dialog close (WCAG 2.4.3)', () => {
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      cb(0);
      return 0;
    });
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    trigger.focus();
    const triggerRef = { current: trigger };
    const { unmount } = render(
      <NextIntlClientProvider locale="en" messages={{ account: enAccount }}>
        <Dialog titleId="t" onClose={vi.fn()} restoreFocusTo={triggerRef}>
          <h2 id="t">x</h2>
          <button>inside</button>
        </Dialog>
      </NextIntlClientProvider>,
    );
    expect(document.activeElement).not.toBe(trigger); // focus moved into dialog
    unmount();
    expect(document.activeElement).toBe(trigger); // restored
    vi.unstubAllGlobals();
    trigger.remove();
  });
});

// ============================================================
// AC2: ApiKeyDisplay (component, plaintext discipline)
// ============================================================

describe('AC2: Create — ApiKeyDisplay (component, plaintext discipline)', () => {
  function renderDisplay() {
    return renderIntl(<ApiKeyDisplay plaintext={CANONICAL_PLAINTEXT} apiKeyId="abc" locale="en" />);
  }

  test('5.5-INT-010: masked on mount ("he-"+43•); plaintext NOT in initial DOM text', () => {
    const { container } = renderDisplay();
    expect(container.textContent).not.toContain(CANONICAL_PLAINTEXT);
    expect(container.textContent).toContain(`he-${'•'.repeat(43)}`);
  });
  test('5.5-INT-011: [Reveal] toggles masked<->unmasked; aria-pressed tracks state', async () => {
    const { container } = renderDisplay();
    const reveal = screen.getByRole('button', { name: 'Reveal' });
    expect(reveal).toHaveAttribute('aria-pressed', 'false');
    await userEvent.click(reveal);
    expect(container.querySelector('code')?.textContent).toBe(CANONICAL_PLAINTEXT);
    expect(screen.getByRole('button', { name: 'Hide' })).toHaveAttribute('aria-pressed', 'true');
  });
  test('5.5-INT-012: aria-live announces plaintext on FIRST reveal only (latched, BR-A11Y-3)', async () => {
    const { container } = renderDisplay();
    const live = container.querySelector('.sr-only[aria-live="polite"]')!;
    expect(live.textContent).toBe('');
    await userEvent.click(screen.getByRole('button', { name: 'Reveal' }));
    expect(live.textContent).toBe(CANONICAL_PLAINTEXT);
    await userEvent.click(screen.getByRole('button', { name: 'Hide' }));
    expect(live.textContent).toBe(CANONICAL_PLAINTEXT); // latched — no re-announce
  });
  test('5.5-INT-013: [Copy] invokes navigator.clipboard.writeText(plaintext) → "Copied" toast', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    renderDisplay();
    await userEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(writeText).toHaveBeenCalledWith(CANONICAL_PLAINTEXT);
    expect(await screen.findByText('API key copied to clipboard.')).toBeInTheDocument();
  });
  test("5.5-INT-014: [I've saved it] calls router.replace(\"/[locale]/keys\") (BR-C-3 scrub)", async () => {
    renderDisplay();
    await userEvent.click(screen.getByRole('button', { name: "I've saved it" }));
    expect(routerMock.replace).toHaveBeenCalledWith('/en/keys');
  });
  test('5.5-INT-015 / 5.5-SEC-003: HTML escape — name "<script>" rendered as literal, no script element', () => {
    const { container } = renderIntl(
      <KeysTable
        keys={[makeRow({ name: '<script>alert(1)</script>' })]}
        locale="en"
        onConfigure={vi.fn()}
        onRevoke={vi.fn()}
      />,
    );
    expect(container.querySelector('script')).toBeNull();
    expect(screen.getAllByText('<script>alert(1)</script>').length).toBeGreaterThan(0);
  });
  test('[BLIND-SPOT] 5.5-BLIND-ERROR-005: clipboard write rejected → "Copy failed" toast; plaintext still visible', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'));
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const { container } = renderDisplay();
    await userEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByText(/Copy failed/)).toBeInTheDocument();
    expect(container.querySelector('code')).not.toBeNull();
  });
});

// ============================================================
// AC3: Configure — money helpers
// ============================================================

describe('AC3: Configure — money.ts (financial correctness, BR-U-5)', () => {
  test('5.5-UNIT-020: parseDecimal("50.00","en") → {value:"50.00"}; formatDecimal round-trips', () => {
    expect(parseDecimal('50.00', 'en')).toEqual({ value: '50.00', error: null });
    expect(formatDecimal('50.00', 'en', 'USD')).toContain('50.00');
  });
  test('5.5-UNIT-021: parseDecimal("50,00","de") → {value:"50.00"} (comma decimal)', () => {
    expect(parseDecimal('50,00', 'de')).toEqual({ value: '50.00', error: null });
  });
  test('5.5-UNIT-022: parseDecimal("٥٠٫٠٠","ar") → {value:"50.00"} (Arabic-Indic digits)', () => {
    expect(parseDecimal('٥٠٫٠٠', 'ar')).toEqual({ value: '50.00', error: null });
  });
  test('5.5-UNIT-023: toCanonicalString always "NN.NN" (2dp) regardless of locale', () => {
    expect(toCanonicalString('50')).toBe('50.00');
    expect(toCanonicalString('50.5')).toBe('50.50');
  });
  test('5.5-UNIT-024: parseDecimal("1,000.00","en") → {value:"1000.00"} (grouping stripped)', () => {
    expect(parseDecimal('1,000.00', 'en')).toEqual({ value: '1000.00', error: null });
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-001: parseDecimal("0.01","en") → accepted (min cap)', () => {
    expect(parseDecimal('0.01', 'en')).toEqual({ value: '0.01', error: null });
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-002: parseDecimal("999999.99","en") → accepted (max cap)', () => {
    expect(parseDecimal('999999.99', 'en')).toEqual({ value: '999999.99', error: null });
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-003: parseDecimal("0.00","en") → out_of_range', () => {
    expect(parseDecimal('0.00', 'en').error).toBe('out_of_range');
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-004: parseDecimal("1000000.00","en") → out_of_range', () => {
    expect(parseDecimal('1000000.00', 'en').error).toBe('out_of_range');
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-005: parseDecimal("50.123","en") → invalid (3 decimals)', () => {
    expect(parseDecimal('50.123', 'en').error).toBe('invalid');
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-006: parseDecimal("-50.00","en") → out_of_range/invalid', () => {
    expect(parseDecimal('-50.00', 'en').error).not.toBeNull();
  });
  test('[BLIND-SPOT] 5.5-BLIND-BOUNDARY-007: parseDecimal("","en") → {value:null} (empty = no cap)', () => {
    expect(parseDecimal('', 'en')).toEqual({ value: null, error: null });
  });
});

describe('AC3: Configure — IP/CIDR validator (BR-U-4, Q-IPV6)', () => {
  test('5.5-UNIT-030: accepts IPv4 / IPv6 / IPv4-CIDR / IPv6-CIDR', () => {
    for (const ip of ['192.168.1.1', '2001:db8::1', '192.168.1.0/24', '2001:db8::/32']) {
      expect(validateIpRule(ip).valid).toBe(true);
    }
  });
  test('5.5-UNIT-031: rejects 0.0.0.0/0 and ::/0 (degenerate CIDR)', () => {
    expect(validateIpRule('0.0.0.0/0').error).toBe('degenerate_cidr');
    expect(validateIpRule('::/0').error).toBe('degenerate_cidr');
  });
  test('5.5-UNIT-032: rejects IPv6 zone-id fe80::1%eth0', () => {
    expect(validateIpRule('fe80::1%eth0').error).toBe('zone_id_rejected');
  });
  test('5.5-UNIT-033: rejects garbage (999.1.1.1, not-an-ip) → invalid_format', () => {
    expect(validateIpRule('999.1.1.1').error).toBe('invalid_format');
    expect(validateIpRule('not-an-ip').error).toBe('invalid_format');
  });
});

// ============================================================
// AC3: diff-patch + ConfigureKeyDrawer / IpWhitelistEditor (component)
// ============================================================

describe('AC3: Configure — diff-patch + all-models (BR-U-2/U-6)', () => {
  const okUpdate = { ok: true as const, response: { ...makeRow() } as never };

  function renderDrawer(keyEntry: KeyEntry, availableModels: string[] = ['qwen-max', 'deepseek-v3']) {
    const props = { onClose: vi.fn(), notify: vi.fn(), onMutated: vi.fn() };
    renderIntl(
      <ConfigureKeyDrawer keyEntry={keyEntry} locale="en" availableModels={availableModels} {...props} />,
    );
    return props;
  }

  test('5.5-UNIT-034: dirtyFields → minimal PATCH (cap-only / models-only)', async () => {
    updateMyKeyMock.mockResolvedValue(okUpdate);
    // cap-only: key with no cap → enable input then set "50.00"
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('No cap')); // uncheck → enable input
    await userEvent.type(screen.getByLabelText('Monthly cost cap in USD'), '50.00');
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(updateMyKeyMock.mock.calls[0]![0].patch).toEqual({ monthly_cost_cap_usd: '50.00' });
    cleanup();
    updateMyKeyMock.mockClear();

    // models-only: key with all-models → pick qwen-max
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('All models allowed')); // toggle OFF
    await userEvent.click(screen.getByLabelText('qwen-max'));
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(updateMyKeyMock.mock.calls[0]![0].patch).toEqual({ scope: { models: ['qwen-max'] } });
  });

  test('5.5-UNIT-035: "All models" ON serialises to scope.models:[] (canonical no-restriction)', async () => {
    updateMyKeyMock.mockResolvedValue(okUpdate);
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: ['qwen-max'], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('All models allowed')); // toggle ON
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(updateMyKeyMock.mock.calls[0]![0].patch).toEqual({ scope: { models: [] } });
  });
});

describe('AC3: Configure — ConfigureKeyDrawer / IpWhitelistEditor (component)', () => {
  function renderDrawer(keyEntry: KeyEntry, availableModels: string[] = ['qwen-max', 'deepseek-v3']) {
    const props = { onClose: vi.fn(), notify: vi.fn(), onMutated: vi.fn() };
    renderIntl(
      <ConfigureKeyDrawer keyEntry={keyEntry} locale="en" availableModels={availableModels} {...props} />,
    );
    return props;
  }

  test('5.5-INT-020: drawer opens with current row state preloaded (no separate GET, BR-U-1)', () => {
    renderDrawer(makeRow({ monthly_cost_cap_usd: '50.00', scope: { models: ['qwen-max'], ip_whitelist: ['1.2.3.4'] } }));
    expect(screen.getByLabelText('Monthly cost cap in USD')).toHaveValue('50.00');
    expect(screen.getByLabelText('qwen-max')).toBeChecked();
    expect(screen.getByDisplayValue('1.2.3.4')).toBeInTheDocument();
  });
  test('5.5-INT-021: Save disabled while pristine / invalid (BR-U-3)', async () => {
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    const save = screen.getByRole('button', { name: 'Save changes' });
    expect(save).toBeDisabled(); // pristine
    await userEvent.click(screen.getByRole('button', { name: 'Add IP' }));
    await userEvent.type(screen.getByLabelText('IP 1'), '999.1.1.1');
    expect(save).toBeDisabled(); // invalid IP
  });
  test('5.5-INT-022: "No cap" toggle clears+disables input; wire value null (BR-U-5)', async () => {
    updateMyKeyMock.mockResolvedValue({ ok: true, response: makeRow() as never });
    const { onMutated } = renderDrawer(makeRow({ monthly_cost_cap_usd: '50.00', scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('No cap'));
    const capInput = screen.getByLabelText('Monthly cost cap in USD');
    expect(capInput).toBeDisabled();
    expect(capInput).toHaveValue('');
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(updateMyKeyMock.mock.calls[0]![0].patch).toEqual({ monthly_cost_cap_usd: null });
    expect(onMutated).toHaveBeenCalled();
  });
  test('5.5-INT-023: IpWhitelistEditor add appends row; remove deletes row', async () => {
    const onChange = vi.fn();
    renderIntl(<IpWhitelistEditor rows={['1.2.3.4']} rowErrors={[null]} onChange={onChange} />);
    await userEvent.click(screen.getByRole('button', { name: 'Add another IP' }));
    expect(onChange).toHaveBeenLastCalledWith(['1.2.3.4', '']);
    await userEvent.click(screen.getByRole('button', { name: 'Remove IP 1.2.3.4' }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });
  test('5.5-INT-024: whitespace-only IP rows silently dropped on submit (BR-U-4)', async () => {
    updateMyKeyMock.mockResolvedValue({ ok: true, response: makeRow() as never });
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByRole('button', { name: 'Add IP' }));
    await userEvent.type(screen.getByLabelText('IP 1'), '1.2.3.4');
    await userEvent.click(screen.getByRole('button', { name: 'Add another IP' }));
    await userEvent.type(screen.getByLabelText('IP 2'), '   ');
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(updateMyKeyMock.mock.calls[0]![0].patch).toEqual({ scope: { ip_whitelist: ['1.2.3.4'] } });
  });
  test('5.5-INT-025: unsaved-changes guard — dirty close → "Discard?" dialog (BR-U-8)', async () => {
    renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('No cap')); // enable input
    await userEvent.type(screen.getByLabelText('Monthly cost cap in USD'), '50.00');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.getByText('Discard unsaved changes?')).toBeInTheDocument();
  });
  test('[BLIND-SPOT] 5.5-BLIND-ERROR-004: /public/models fetch fails → selector disabled+tooltip; cap still editable (BR-U-7)', () => {
    renderDrawer(makeRow({ scope: { models: ['qwen-max'], ip_whitelist: [] } }), []); // empty catalogue
    expect(screen.getByText('Model catalogue unavailable — try again later.')).toBeInTheDocument();
    expect(screen.getByLabelText('Monthly cost cap in USD')).toBeEnabled();
  });
  test('[BLIND-SPOT] 5.5-BLIND-DATA-003: configure 400 → form retains input + error toast + NO row mutation', async () => {
    updateMyKeyMock.mockResolvedValue({ ok: false, error: { code: 'account.keys.edit.errors.invalid_request' } });
    const { onMutated, onClose } = renderDrawer(makeRow({ monthly_cost_cap_usd: null, scope: { models: [], ip_whitelist: [] } }));
    await userEvent.click(screen.getByLabelText('No cap')); // enable input
    await userEvent.type(screen.getByLabelText('Monthly cost cap in USD'), '50.00');
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateMyKeyMock).toHaveBeenCalled());
    expect(await screen.findByText(/configuration values are invalid/i)).toBeInTheDocument();
    expect(screen.getByLabelText('Monthly cost cap in USD')).toHaveValue('50.00'); // retained
    expect(onMutated).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });
  test('[BLIND-SPOT] 5.5-BLIND-DATA-002 (GAP-CAP-001): current=0,cap=50 → heuristic renders GREEN/no-badge', () => {
    const { container } = renderIntl(<CapBudgetBar current="0.00" cap="50.00" locale="en" />);
    expect(container.querySelector('.bg-green-500')).not.toBeNull();
    expect(screen.queryByText('Tripped')).toBeNull();
  });
});

// ============================================================
// AC4: Revoke — type-name match, RevokeKeyDialog
// ============================================================

describe('AC4: Revoke — type-name-to-confirm match (BR-R-1)', () => {
  function renderRevoke(name: string) {
    const props = { onClose: vi.fn(), notify: vi.fn(), onMutated: vi.fn() };
    renderIntl(<RevokeKeyDialog keyEntry={makeRow({ name })} {...props} />);
    return props;
  }
  function revokeBtn() {
    return screen.getByRole('button', { name: 'Revoke' });
  }

  test('5.5-UNIT-040: exact typed===name (NFC both sides) enables [Revoke]', async () => {
    renderRevoke('My First Key');
    expect(revokeBtn()).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Type the key name My First Key/), 'My First Key');
    expect(revokeBtn()).toBeEnabled();
  });
  test('5.5-UNIT-041: case-different ("my first key" vs "My First Key") → disabled', async () => {
    renderRevoke('My First Key');
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'my first key');
    expect(revokeBtn()).toBeDisabled();
  });
  test('5.5-UNIT-042: precomposed-é vs decomposed-é → matches (NFC both sides)', async () => {
    renderRevoke('café'); // precomposed
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'café'); // decomposed
    expect(revokeBtn()).toBeEnabled();
  });
  test('5.5-UNIT-043: trailing-whitespace typed → no match (disabled)', async () => {
    renderRevoke('My First Key');
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'My First Key ');
    expect(revokeBtn()).toBeDisabled();
  });
});

describe('AC4: Revoke — RevokeKeyDialog (component)', () => {
  function renderRevoke(name: string) {
    const props = { onClose: vi.fn(), notify: vi.fn(), onMutated: vi.fn() };
    renderIntl(<RevokeKeyDialog keyEntry={makeRow({ name, key_prefix: 'he-ZZZ999AAA' })} {...props} />);
    return props;
  }

  test('5.5-INT-030 / 5.5-A11Y-009: role="alertdialog"; [Cancel] default-focus; [Revoke] destructive+disabled-until-match', () => {
    renderRevoke('My Key');
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeDisabled();
  });
  test('5.5-INT-031: displays Name + Prefix for disambiguation (BR-R-8)', () => {
    renderRevoke('My Key');
    expect(screen.getByText('My Key')).toBeInTheDocument();
    expect(screen.getByText(/he-ZZZ999AAA/)).toBeInTheDocument();
  });
  test('5.5-INT-032: was_already_revoked=true → info notify; =false → success notify (BR-R-5)', async () => {
    revokeMyKeyMock.mockResolvedValue({
      ok: true,
      response: { api_key_id: 'x', revoked_at: '2026-06-02T00:00:00Z', was_already_revoked: true },
    });
    const { notify } = renderRevoke('My Key');
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'My Key');
    await userEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(notify).toHaveBeenCalledWith('info', 'account.keys.revoke.already_revoked'));

    cleanup();
    revokeMyKeyMock.mockResolvedValue({
      ok: true,
      response: { api_key_id: 'x', revoked_at: '2026-06-02T00:00:00Z', was_already_revoked: false },
    });
    const { notify: notify2 } = renderRevoke('My Key');
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'My Key');
    await userEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(notify2).toHaveBeenCalledWith('success', 'account.keys.revoke.success'));
  });
  test('5.5-INT-033: close after typing → value discarded (uncontrolled); re-open blank (BR-R-7)', async () => {
    const { unmount } = renderIntl(
      <RevokeKeyDialog keyEntry={makeRow({ name: 'My Key' })} onClose={vi.fn()} notify={vi.fn()} onMutated={vi.fn()} />,
    );
    await userEvent.type(screen.getByLabelText(/Type the key name/), 'My Key');
    unmount();
    renderIntl(
      <RevokeKeyDialog keyEntry={makeRow({ name: 'My Key' })} onClose={vi.fn()} notify={vi.fn()} onMutated={vi.fn()} />,
    );
    expect(screen.getByLabelText(/Type the key name/)).toHaveValue('');
  });
});

// ============================================================
// Cross-cutting error handling (component-level, Vitest)
// ============================================================

describe('Cross-cutting: error handling (component)', () => {
  test('[BLIND-SPOT] 5.5-BLIND-ERROR-003: list malformed JSON → graceful empty list + shape-drift warn (shipped behaviour)', async () => {
    // Architect/shipped correction: listMyKeys degrades to an empty list (NOT an
    // ErrorBoundary throw) and warns on shape drift — the page renders empty-state.
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, json: async () => ({ wrong: 'shape' }) });
    vi.stubGlobal('fetch', fetchMock);
    const result = await listMyKeys();
    expect(result).toEqual({ ok: true, response: { object: 'list', data: [] } });
    expect(warn).toHaveBeenCalled();
    vi.unstubAllGlobals();
    warn.mockRestore();
  });
});
