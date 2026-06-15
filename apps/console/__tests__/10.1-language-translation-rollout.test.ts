/**
 * Story 10.1: 控制台 10 种语言翻译落地 — UNIT + INTEGRATION scenarios (Vitest).
 *
 * Implements the QA test-design skeleton (Turing, 2026-06-15). E2E scenarios live
 * in e2e/2.1-nextjs-console-skeleton-i18n.spec.ts (2.1-E2E-005 unskipped → 10.1-E2E-004).
 * Script-gate scenarios (key parity / placeholder-zero / codegen drift) are run as
 * pure fs+JS checks here (deterministic, no child_process) — they assert exactly
 * what scripts/check-i18n-keys.ts + scripts/gen-i18n-keys.ts enforce in CI.
 *
 * Test Design: docs/qa/assessments/10.1-test-design-20260615.md
 */

import { describe, test, expect, vi } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, extname, basename } from 'node:path';
import { render, screen } from '@testing-library/react';
import { createElement, type ComponentProps } from 'react';
import { NextIntlClientProvider, useTranslations } from 'next-intl';

type ProviderProps = ComponentProps<typeof NextIntlClientProvider>;

import { fallbackMap, isLocale, type Locale } from '@/i18n/config';
import { NAMESPACES } from '@/i18n/namespaces';
import { loadMessages } from '@/i18n/load-messages';
import { resolveLocale, buildLocaleCookieOptions } from '@/lib/i18n';
import { LtrText } from '@/components/business/LtrText';
import enCommon from '@/messages/en/common.json';

// The 4 namespaces whose VALUES this rollout fills (account/logs/models/dashboard).
// auth/common are already fully translated and untouched here; auth.ts codegen is
// pre-existing-stale (the `2fa.*` leading-digit generator bug — see
// project_auth_surface_prebroken_head) and is explicitly out of 10.1's scope.
const ROLLOUT_NAMESPACES = ['account', 'logs', 'models', 'dashboard'] as const;

// process.cwd() is apps/console under vitest.
const CONSOLE_ROOT = process.cwd();
const MESSAGES_DIR = resolve(CONSOLE_ROOT, 'messages');
const I18N_KEYS_SRC = resolve(CONSOLE_ROOT, '../../packages/i18n-keys/src');

// ---- fs helpers -----------------------------------------------------------

type Json = { [k: string]: unknown };
function flatten(obj: Json, prefix = ''): Map<string, unknown> {
  const out = new Map<string, unknown>();
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      for (const [kk, vv] of flatten(v as Json, path)) out.set(kk, vv);
    } else {
      out.set(path, v);
    }
  }
  return out;
}
function readNs(locale: string, ns: string): Map<string, unknown> {
  return flatten(JSON.parse(readFileSync(join(MESSAGES_DIR, locale, `${ns}.json`), 'utf8')));
}
function localeDirs(): string[] {
  return readdirSync(MESSAGES_DIR).filter((d) => {
    try {
      return statSync(join(MESSAGES_DIR, d)).isDirectory();
    } catch {
      return false;
    }
  });
}
function walkTsx(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '__tests__' || entry === '.next') continue;
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) walkTsx(full, acc);
    else if (extname(entry) === '.tsx' || extname(entry) === '.ts') acc.push(full);
  }
  return acc;
}
function placeholders(v: unknown): Set<string> {
  if (typeof v !== 'string') return new Set();
  const out = new Set<string>();
  for (const m of v.matchAll(/\{\s*([a-zA-Z0-9_]+)\s*[,}]/g)) out.add(m[1]!);
  return out;
}

// ============================================================
// AC1: 全 UI 在 10 种语言下完整本地化且可切换
// ============================================================

describe('AC1: 全 UI 10 语言本地化且可切换', () => {
  test('10.1-UNIT-001: request config loads all 6 namespaces per locale', async () => {
    for (const loc of ['ja', 'ar', 'en'] as Locale[]) {
      const messages = await loadMessages(loc);
      expect(Object.keys(messages).sort()).toEqual([...NAMESPACES].sort());
    }
  });

  test('10.1-UNIT-002: loader returns the requested locale content (ja !== en)', async () => {
    const ja = await loadMessages('ja');
    const en = await loadMessages('en');
    // common is fully translated → the two locales' content must differ.
    expect(JSON.stringify(ja.common)).not.toEqual(JSON.stringify(en.common));
  });

  test('10.1-UNIT-003: key-set parity across all 10 locales (check-i18n-keys core)', () => {
    for (const ns of NAMESPACES) {
      const enKeys = new Set(readNs('en', ns).keys());
      for (const loc of localeDirs()) {
        if (loc === 'en') continue;
        const keys = new Set(readNs(loc, ns).keys());
        const missing = [...enKeys].filter((k) => !keys.has(k));
        const extra = [...keys].filter((k) => !enKeys.has(k));
        expect(missing, `${loc}/${ns} missing ${missing.join(', ')}`).toEqual([]);
        expect(extra, `${loc}/${ns} extra ${extra.join(', ')}`).toEqual([]);
      }
    }
  });

  test('10.1-UNIT-004: no [en-pending] placeholder remains in messages/', () => {
    const offenders: string[] = [];
    for (const loc of localeDirs()) {
      for (const entry of readdirSync(join(MESSAGES_DIR, loc))) {
        if (extname(entry) !== '.json') continue;
        const raw = readFileSync(join(MESSAGES_DIR, loc, entry), 'utf8');
        if (raw.includes('en-pending')) offenders.push(`${loc}/${entry}`);
      }
    }
    expect(offenders, `files still carrying [en-pending]: ${offenders.join(', ')}`).toEqual([]);
  });

  test('10.1-UNIT-005: i18n-keys codegen has no drift vs en key set (value-only changes)', () => {
    // gen-i18n-keys derives the union purely from messages/en/*.json. Translating
    // VALUES must not move the KEY set, so each committed src/{ns}.ts must encode
    // exactly en's flattened keys. Scoped to the rollout namespaces — auth.ts is
    // pre-existing-stale (the `2fa.*` codegen bug, documented baseline; not 10.1).
    for (const ns of ROLLOUT_NAMESPACES) {
      const enKeys = new Set(readNs('en', ns).keys());
      const src = readFileSync(join(I18N_KEYS_SRC, `${ns}.ts`), 'utf8');
      const body = src.slice(src.indexOf('='));
      const encoded = new Set([...body.matchAll(/'([^']+)'/g)].map((m) => m[1]!));
      const missing = [...enKeys].filter((k) => !encoded.has(k));
      const extra = [...encoded].filter((k) => !enKeys.has(k));
      expect(missing, `${ns}.ts missing ${missing.join(', ')}`).toEqual([]);
      expect(extra, `${ns}.ts extra ${extra.join(', ')}`).toEqual([]);
    }
  });

  test('10.1-INT-001: full-page SSR under ja has no MISSING_MESSAGE / bare key', async () => {
    const messages = await loadMessages('ja');
    const onError = vi.fn();

    // Probe every namespace via its first leaf key through the REAL loader output
    // (not a hand-injected provider) — the linchpin gap was that only `common`
    // was wired, so account/dashboard/logs/models keys went MISSING.
    const probes = NAMESPACES.map((ns) => {
      const firstKey = [...flatten(messages[ns] as Json).keys()][0]!;
      return { ns, firstKey };
    });
    function Probe() {
      const t = useTranslations();
      return createElement(
        'div',
        null,
        probes.map((p) => createElement('span', { key: p.ns, 'data-ns': p.ns }, t(`${p.ns}.${p.firstKey}`))),
      );
    }
    render(
      createElement(
        NextIntlClientProvider,
        { locale: 'ja', messages, onError } as unknown as ProviderProps,
        createElement(Probe),
      ),
    );
    expect(onError).not.toHaveBeenCalled();
    // None rendered the bare key path.
    for (const p of probes) {
      const cell = screen.getByText((_, el) => el?.getAttribute('data-ns') === p.ns);
      expect(cell.textContent).not.toBe(`${p.ns}.${p.firstKey}`);
    }
  });

  test('10.1-INT-002: i18n/namespaces.ts is the single SoT (matches en directory)', () => {
    const enNamespaces = readdirSync(join(MESSAGES_DIR, 'en'))
      .filter((f) => extname(f) === '.json')
      .map((f) => basename(f, '.json'))
      .sort();
    expect([...NAMESPACES].sort()).toEqual(enNamespaces);
  });

  test('10.1-UNIT-006: Intl date/number/currency formatted per locale', () => {
    expect(new Intl.NumberFormat('ja').format(1234567)).not.toEqual(
      new Intl.NumberFormat('de').format(1234567),
    );
    expect(new Intl.NumberFormat('en', { style: 'currency', currency: 'USD' }).format(12.5)).toContain('$');
    expect(new Intl.DateTimeFormat('ja', { dateStyle: 'long' }).format(new Date(Date.UTC(2026, 5, 15)))).toContain(
      '2026',
    );
  });

  test('10.1-UNIT-007: fallback chain user -> variant -> en', () => {
    expect(fallbackMap['zh-TW']).toBe('zh-CN');
    expect(fallbackMap['pt-BR']).toBe('pt');
    expect(resolveLocale({ cookieValue: 'zh-TW', acceptLanguage: null })).toBe('zh-CN');
    expect(resolveLocale({ cookieValue: 'pt-BR', acceptLanguage: null })).toBe('pt');
    expect(resolveLocale({ cookieValue: undefined, acceptLanguage: null })).toBe('en');
  });

  test('10.1-INT-003: cookie-first then Accept-Language; he_locale persists', () => {
    // cookie wins over a conflicting header…
    expect(resolveLocale({ cookieValue: 'ja', acceptLanguage: 'fr,en;q=0.9' })).toBe('ja');
    // …no cookie → Accept-Language…
    expect(resolveLocale({ cookieValue: undefined, acceptLanguage: 'de-DE,de;q=0.9' })).toBe('de');
    // …choice persisted under the he_locale cookie.
    expect(buildLocaleCookieOptions('production').name).toBe('he_locale');
  });

  test('10.1-INT-006: a11y not regressed — LocaleSwitch aria + status non-color-only', async () => {
    vi.resetModules();
    vi.doMock('next/navigation', () => ({ usePathname: () => '/en/dashboard' }));
    vi.doMock('@/app/[locale]/_actions/locale', () => ({ setLocale: vi.fn() }));
    const { LocaleSwitch } = await import('@/components/LocaleSwitch');

    render(
      createElement(
        NextIntlClientProvider,
        { locale: 'en', messages: { common: enCommon } } as unknown as ProviderProps,
        createElement(LocaleSwitch, { currentLocale: 'en' }),
      ),
    );
    const label = (enCommon as { localeSwitch: { label: string } }).localeSwitch.label;
    const trigger = screen.getByRole('button', { name: label });
    expect(trigger).toBeInTheDocument();
    expect(trigger.getAttribute('aria-label')).not.toMatch(/localeSwitch\.label/); // not a bare key

    vi.doUnmock('next/navigation');
    vi.doUnmock('@/app/[locale]/_actions/locale');
  });

  // --- Blind Spot Scenarios ---

  test('[BLIND-SPOT] 10.1-BLIND-BOUNDARY-001: unknown locale route -> notFound()', () => {
    // The isLocale guard must survive the new all-namespace wiring: an unsupported
    // locale is rejected and request.ts still calls notFound() before loading.
    expect(isLocale('xx')).toBe(false);
    expect(isLocale('ar')).toBe(true);
    const src = readFileSync(resolve(CONSOLE_ROOT, 'i18n/request.ts'), 'utf8');
    expect(src).toMatch(/if\s*\(\s*!isLocale\(requested\)\s*\)/);
    expect(src).toMatch(/notFound\(\)/);
  });

  test('[BLIND-SPOT] 10.1-BLIND-BOUNDARY-002: ICU placeholder/plural isomorphic to en', () => {
    for (const ns of NAMESPACES) {
      const en = readNs('en', ns);
      for (const loc of localeDirs()) {
        if (loc === 'en') continue;
        const flat = readNs(loc, ns);
        for (const [k, enVal] of en) {
          const enPh = placeholders(enVal);
          if (enPh.size === 0) continue;
          const locPh = placeholders(flat.get(k));
          expect([...locPh].sort(), `${loc}/${ns} @ ${k} placeholder drift`).toEqual([...enPh].sort());
        }
      }
    }
  });

  test('[BLIND-SPOT] 10.1-BLIND-ERROR-001: missing/malformed namespace fails safe (no swallow)', async () => {
    // A missing namespace file rejects the dynamic import (surfaced, not swallowed).
    // Built at runtime so Vite doesn't try to statically resolve it at collection.
    const missing = '../messages/en/__does_not_exist__' + '.json';
    await expect(import(/* @vite-ignore */ missing)).rejects.toBeTruthy();
    // …and the loader has no try/catch that would silently swallow such a failure.
    const src = readFileSync(resolve(CONSOLE_ROOT, 'i18n/load-messages.ts'), 'utf8');
    expect(src).not.toMatch(/catch\s*\(/);
  });
});

// ============================================================
// AC2: 阿拉伯文（ar）RTL 布局正确 (mechanical)
// ============================================================

describe('AC2: ar RTL 布局正确 (mechanical)', () => {
  test('10.1-UNIT-008: zero RTL-unsafe physical direction classes remain', () => {
    const DIRECTION_RE = /\b(pl-\d|pr-\d|ml-\d|mr-\d|ml-auto|mr-auto|text-left|text-right|left-\d|right-\d)/;
    const offenders: string[] = [];
    for (const dir of [resolve(CONSOLE_ROOT, 'app'), resolve(CONSOLE_ROOT, 'components')]) {
      for (const file of walkTsx(dir)) {
        const src = readFileSync(file, 'utf8');
        // only inspect className string literals to avoid matching prose/comments
        for (const m of src.matchAll(/className=(?:"([^"]*)"|'([^']*)'|\{`([^`]*)`\})/g)) {
          const classes = m[1] ?? m[2] ?? m[3] ?? '';
          if (DIRECTION_RE.test(classes)) offenders.push(`${basename(file)}: ${classes.match(DIRECTION_RE)?.[0]}`);
        }
      }
    }
    expect(offenders, `RTL-unsafe physical classes: ${offenders.join(' | ')}`).toEqual([]);
  });

  test('10.1-UNIT-009: LtrText renders dir="ltr" wrapper, content not reordered', () => {
    const { container } = render(createElement(LtrText, null, 'req_abc123'));
    const el = container.querySelector('[dir="ltr"]');
    expect(el).not.toBeNull();
    expect(el?.textContent).toBe('req_abc123');
  });

  test('10.1-INT-005: global [dir=rtl] icon-mirror CSS present', () => {
    const css = readFileSync(resolve(CONSOLE_ROOT, 'app/globals.css'), 'utf8');
    expect(css).toMatch(/\[dir=['"]?rtl['"]?\]\s+\.rtl-mirror/);
    expect(css).toContain('scaleX(-1)');
  });

  // E2E (implemented in e2e/2.1-nextjs-console-skeleton-i18n.spec.ts):
  test.todo('[BLIND-SPOT] 10.1-BLIND-BOUNDARY-003: ar number/ID/timestamp stays LTR — see e2e spec');
  test.todo('[BLIND-SPOT] 10.1-BLIND-FLOW-001: rapid repeated locale switch, no stale — see e2e spec');
  test.todo('[BLIND-SPOT] 10.1-BLIND-FLOW-002: en->ar->en dir toggles cleanly — see e2e spec');
});
