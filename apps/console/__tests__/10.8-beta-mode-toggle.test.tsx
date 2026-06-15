/**
 * Story 10.8: Beta 公测开关 + 限额免费试用配置 (AC2 — console surface).
 *
 * Implements the QA test design (Turing, 2026-06-16). Scope: ratified (b) — no
 * backend money code. This file covers AC2 (console BetaBadge + i18n + anti-orphan
 * mount + go-live checklist gate + docs-site Beta marking). AC1 backend-verification
 * + no-money-path guards live in Go (packages/plan-catalogue/catalogue_test.go,
 * apps/api-gateway/internal/entitlement/beta_trial_1008_test.go) + the static guard
 * scripts/ci/verify-no-money-path-10.8.sh.
 *
 * Test Design: docs/qa/assessments/10.8-test-design-20260616.md
 */

import { readFileSync, existsSync } from 'node:fs';
import { resolve, join } from 'node:path';

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { NextIntlClientProvider } from 'next-intl';
import type { AbstractIntlMessages } from 'next-intl';

import { BetaBadge } from '@/components/business/BetaBadge';
import { parseBetaFlag } from '@/lib/beta/mode';
import enBeta from '@/messages/en/beta.json';

// process.cwd() is apps/console under vitest (matches the 10.1 rollout test).
const CONSOLE_ROOT = process.cwd();
const REPO_ROOT = resolve(CONSOLE_ROOT, '../..');
const MESSAGES_DIR = resolve(CONSOLE_ROOT, 'messages');
const LOCALES = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'ko', 'pt', 'ru', 'zh-CN'] as const;

const BETA_ENV = 'NEXT_PUBLIC_BETA_MODE';

function renderBadge(locale: string, messages: AbstractIntlMessages = enBeta) {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ beta: messages }}>
      <BetaBadge />
    </NextIntlClientProvider>,
  );
}

// flatten a nested JSON message object to dotted keys (mirrors check-i18n-keys.ts)
function flatten(obj: Record<string, unknown>, prefix = ''): Map<string, unknown> {
  const out = new Map<string, unknown>();
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      for (const [kk, vv] of flatten(v as Record<string, unknown>, path)) out.set(kk, vv);
    } else {
      out.set(path, v);
    }
  }
  return out;
}

function localeBetaKeys(locale: string): Set<string> {
  const raw = JSON.parse(readFileSync(join(MESSAGES_DIR, locale, 'beta.json'), 'utf8'));
  return new Set(flatten(raw).keys());
}

function setsEqual(a: Set<string>, b: Set<string>): boolean {
  if (a.size !== b.size) return false;
  for (const k of a) if (!b.has(k)) return false;
  return true;
}

beforeEach(() => {
  vi.unstubAllEnvs();
});
afterEach(() => {
  cleanup();
  vi.unstubAllEnvs();
});

// ============================================================
// AC2: Beta 模式消费 + 对外「Beta」标识
// ============================================================

describe('AC2: console BetaBadge', () => {
  // --- Core Scenarios ---

  test('10.8-UNIT-010: BetaBadge renders when beta env ON', () => {
    vi.stubEnv(BETA_ENV, 'true');
    renderBadge('en');
    const badge = screen.getByRole('status');
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveTextContent('Beta');
    expect(badge).toHaveAttribute('aria-label', enBeta.badge.ariaLabel);
  });

  test('10.8-UNIT-011: BetaBadge hidden when beta env OFF (GA)', () => {
    vi.stubEnv(BETA_ENV, 'false');
    const { container } = renderBadge('en');
    expect(screen.queryByRole('status')).toBeNull();
    expect(container).toBeEmptyDOMElement();
  });

  test('10.8-INT-010: i18n completeness — Beta key in all 10 locales + en fallback', () => {
    const enKeys = localeBetaKeys('en');
    expect(enKeys.has('badge.label')).toBe(true);
    expect(enKeys.has('badge.ariaLabel')).toBe(true);
    for (const locale of LOCALES) {
      const keys = localeBetaKeys(locale);
      expect(setsEqual(keys, enKeys), `${locale}/beta.json key drift vs en`).toBe(true);
      // every value is a non-empty string (no blank placeholder)
      const raw = JSON.parse(readFileSync(join(MESSAGES_DIR, locale, 'beta.json'), 'utf8'));
      for (const v of flatten(raw).values()) {
        expect(typeof v === 'string' && (v as string).length > 0).toBe(true);
      }
    }
  });

  test('10.8-INT-011: BetaBadge mounted in console layout/shell (anti-orphan)', () => {
    const layout = readFileSync(
      resolve(CONSOLE_ROOT, 'app/[locale]/(console)/layout.tsx'),
      'utf8',
    );
    expect(layout).toMatch(/import\s*\{\s*BetaBadge\s*\}\s*from\s*['"]@\/components\/business\/BetaBadge['"]/);
    expect(layout).toMatch(/<BetaBadge\s*\/>/);
  });

  test('10.8-INT-012: go-live-checklist has GA "drop Beta badge" step', () => {
    const checklist = readFileSync(
      resolve(REPO_ROOT, 'docs/runbooks/go-live-checklist.md'),
      'utf8',
    );
    expect(checklist).toMatch(/drop.*badge|摘除.*角标|Beta 角标/i);
    // the step must tie the drop to a GA rebuild/redeploy (env-driven coupling)
    expect(checklist).toMatch(/rebuild|redeploy/i);
  });

  test('10.8-UNIT-012: negative guard — no He-API beta_mode write surface introduced', () => {
    // The console Beta surface is display-only: it reads a build-time env, never
    // writes/flips the flag (Q-ADMIN-BETA — Unleash-only flip). The repo-wide
    // static guard enforces the same across all source.
    const badgeSrc = readFileSync(resolve(CONSOLE_ROOT, 'components/business/BetaBadge.tsx'), 'utf8');
    const modeSrc = readFileSync(resolve(CONSOLE_ROOT, 'lib/beta/mode.ts'), 'utf8');
    for (const src of [badgeSrc, modeSrc]) {
      expect(src).not.toMatch(/(Set|Update|Flip|Toggle|Write|Enable|Disable)BetaMode/);
      expect(src).not.toMatch(/fetch\s*\(|method:\s*['"]POST['"]/);
    }
    expect(existsSync(resolve(REPO_ROOT, 'scripts/ci/verify-no-money-path-10.8.sh'))).toBe(true);
  });

  test('10.8-E2E-001: console in beta_mode shows Beta badge in current locale', () => {
    // Component-level smoke (flag-protected, low-risk): the badge renders the
    // localized label for a non-en locale when Beta is ON.
    vi.stubEnv(BETA_ENV, '1');
    const zhBeta = JSON.parse(readFileSync(join(MESSAGES_DIR, 'zh-CN', 'beta.json'), 'utf8'));
    renderBadge('zh-CN', zhBeta);
    expect(screen.getByRole('status')).toHaveTextContent(zhBeta.badge.label);
  });

  test('10.8-INT-013: docs site (10.5) carries Beta marking', () => {
    const docsConfig = readFileSync(resolve(REPO_ROOT, 'apps/docs/docusaurus.config.ts'), 'utf8');
    expect(docsConfig).toMatch(/announcementBar/);
    expect(docsConfig).toMatch(/Beta/);
  });

  // --- Blind Spot Scenarios ---

  test('[BLIND-SPOT] 10.8-BLIND-BOUNDARY-001: beta env unset/empty → OFF, no crash', () => {
    // pure parse: unset + empty both fail-safe to OFF
    expect(parseBetaFlag(undefined)).toBe(false);
    expect(parseBetaFlag('')).toBe(false);
    expect(parseBetaFlag('   ')).toBe(false);

    // unset env → hidden, no throw
    vi.stubEnv(BETA_ENV, '');
    expect(() => renderBadge('en')).not.toThrow();
    expect(screen.queryByRole('status')).toBeNull();
  });

  test('[BLIND-SPOT] 10.8-BLIND-FLOW-001: runtime GA flip does NOT auto-hide env badge', () => {
    // The badge is driven by the BUILD-TIME env (process.env.NEXT_PUBLIC_BETA_MODE),
    // not the runtime flag:beta_mode gate — so a runtime Unleash GA flip cannot
    // auto-hide it. The documented mitigation is the go-live checklist drop step.
    const modeSrc = readFileSync(resolve(CONSOLE_ROOT, 'lib/beta/mode.ts'), 'utf8');
    expect(modeSrc).toMatch(/process\.env\.NEXT_PUBLIC_BETA_MODE/);
    // no runtime flag read (no fetch / no flag:beta_mode key in the console badge path)
    const badgeSrc = readFileSync(resolve(CONSOLE_ROOT, 'components/business/BetaBadge.tsx'), 'utf8');
    expect(badgeSrc).not.toMatch(/flag:beta_mode|fetch\s*\(/);

    // mitigation present (cross-ref INT-012)
    const checklist = readFileSync(resolve(REPO_ROOT, 'docs/runbooks/go-live-checklist.md'), 'utf8');
    expect(checklist).toMatch(/(rebuild|redeploy).*badge|badge.*(rebuild|redeploy)|摘除.*角标/i);
  });

  test('[BLIND-SPOT] 10.8-BLIND-ERROR-001: missing locale key fails i18n gate', () => {
    // The completeness gate is key-set equality (NOT a silent en fallback): a
    // locale missing the badge key yields a key-set inequality the gate reports
    // as FAIL. Simulate a dropped key and assert the invariant catches it.
    const enKeys = localeBetaKeys('en');
    const broken = new Set(enKeys);
    broken.delete('badge.label');
    expect(setsEqual(enKeys, broken)).toBe(false);
  });
});
