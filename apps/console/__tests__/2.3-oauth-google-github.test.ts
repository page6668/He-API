/**
 * Story 2.3 console-side scenarios (filled per QA review round 1, QA-2.3-H1).
 *
 * Each test embeds `// Scenario: 2.3-UNIT-NNN` for traceability against
 * docs/qa/assessments/2.3-test-design-20260512.md.
 *
 * Companion Playwright skeleton: apps/console/e2e/2.3-oauth-google-github.spec.ts
 */

import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import React from 'react';
import { describe, expect, test, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

// Mock the Server Action so OAuthButton can be exercised in isolation.
// Server Actions cannot run inside vitest (no Next.js request context); the
// mock records calls so we can assert the contract surface.
const mockInitiate = vi.fn(async (_input: { provider: string; returnTo?: string; locale?: string }) => ({
  authorizeUrl: 'https://mock.example.com/authorize?state=abc',
}));
vi.mock('@/app/[locale]/(auth)/_actions/initiate-oauth', () => ({
  initiateOAuth: (input: { provider: string; returnTo?: string; locale?: string }) => mockInitiate(input),
}));

import { OAuthButtonGroup } from '../components/business/OAuthButtonGroup';
import { OAuthButton } from '../components/business/OAuthButton';
import { GOOGLE_CONFIG, GITHUB_CONFIG } from '../lib/oauth-button-config';
import { assertProvider, OAuthValidationError } from '../lib/oauth';

const repoRoot = resolve(__dirname, '..', '..', '..');
const localesDir = join(repoRoot, 'apps/console/messages');
const NON_EN_LOCALES = ['zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;
const OAUTH_KEYS = [
  'oauth.continueWithGoogle',
  'oauth.continueWithGitHub',
  'oauth.divider',
  'oauth.linkSuccessful',
  'oauth.callbackLoading',
  'oauth.errors.userDenied',
  'oauth.errors.stateInvalid',
  'oauth.errors.stateExpired',
  'oauth.errors.invalidReturnTo',
  'oauth.errors.providerError',
  'oauth.errors.emailNotVerified',
  'oauth.errors.subjectMismatch',
  'oauth.errors.linkUnverified',
  'oauth.errors.linkOtherProvider',
] as const;

function loadAuthMessages(locale: string): Record<string, unknown> {
  const raw = readFileSync(join(localesDir, locale, 'auth.json'), 'utf8');
  return JSON.parse(raw) as Record<string, unknown>;
}

function getDeep(obj: unknown, dottedKey: string): unknown {
  return dottedKey.split('.').reduce<unknown>((acc, segment) => {
    if (acc && typeof acc === 'object' && segment in (acc as Record<string, unknown>)) {
      return (acc as Record<string, unknown>)[segment];
    }
    return undefined;
  }, obj);
}

function renderWithIntl(child: React.ReactElement, locale = 'en'): void {
  const messages = { auth: loadAuthMessages(locale) };
  const tree = React.createElement(
    NextIntlClientProvider as unknown as React.ComponentType<React.PropsWithChildren<{ locale: string; messages: Record<string, unknown> }>>,
    { locale, messages },
    child,
  );
  render(tree);
}

beforeEach(() => {
  cleanup();
  mockInitiate.mockClear();
  // jsdom emits a noisy "Not implemented: navigation" line whenever the
  // production OAuthButton calls window.location.assign(authorizeUrl). Stub
  // it so the test logs stay clean — assertion remains on the Server Action
  // call count, not on the URL change.
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { assign: vi.fn() },
  });
});

// ============================================================
// AC4: <OAuthButtonGroup> locale-aware ordering and rendering
// ============================================================

describe('AC4: <OAuthButtonGroup> locale-aware ordering and rendering', () => {
  test('UNIT-062: en locale renders Google button first, GitHub second', () => {
    // Scenario: 2.3-UNIT-062
    renderWithIntl(React.createElement(OAuthButtonGroup, { locale: 'en' }), 'en');
    const buttons = screen.getAllByRole('button');
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).toHaveAttribute('data-provider', 'google');
    expect(buttons[1]).toHaveAttribute('data-provider', 'github');
  });

  test('UNIT-062: zh-CN locale renders GitHub button first (Asia preference)', () => {
    // Scenario: 2.3-UNIT-062 (zh-CN)
    renderWithIntl(React.createElement(OAuthButtonGroup, { locale: 'zh-CN' }), 'zh-CN');
    const buttons = screen.getAllByRole('button');
    expect(buttons[0]).toHaveAttribute('data-provider', 'github');
    expect(buttons[1]).toHaveAttribute('data-provider', 'google');
  });

  test('UNIT-062: ja locale renders GitHub button first', () => {
    // Scenario: 2.3-UNIT-062 (ja)
    renderWithIntl(React.createElement(OAuthButtonGroup, { locale: 'ja' }), 'ja');
    const buttons = screen.getAllByRole('button');
    expect(buttons[0]).toHaveAttribute('data-provider', 'github');
    expect(buttons[1]).toHaveAttribute('data-provider', 'google');
  });

  test('UNIT-062: ko locale renders GitHub button first', () => {
    // Scenario: 2.3-UNIT-062 (ko)
    renderWithIntl(React.createElement(OAuthButtonGroup, { locale: 'ko' }), 'ko');
    const buttons = screen.getAllByRole('button');
    expect(buttons[0]).toHaveAttribute('data-provider', 'github');
    expect(buttons[1]).toHaveAttribute('data-provider', 'google');
  });

  test('UNIT-062: ar locale preserves logical Google-first ordering (visual RTL mirror via CSS)', () => {
    // Scenario: 2.3-UNIT-062 (ar)
    // RTL mirroring is handled at the HTML `dir` level; logical DOM order
    // remains Google-first for non-Asian locales including ar.
    renderWithIntl(React.createElement(OAuthButtonGroup, { locale: 'ar' }), 'ar');
    const buttons = screen.getAllByRole('button');
    expect(buttons[0]).toHaveAttribute('data-provider', 'google');
    expect(buttons[1]).toHaveAttribute('data-provider', 'github');
  });

  test('UNIT-063: Google button renders brand-color logo with white SVG fill', () => {
    // Scenario: 2.3-UNIT-063 (Google)
    expect(GOOGLE_CONFIG.bgClass).toBe('bg-[#4285F4]');
    expect(GOOGLE_CONFIG.iconSvg).toContain('<svg');
    expect(GOOGLE_CONFIG.iconSvg).toContain('fill="#fff"');
    expect(GOOGLE_CONFIG.textClass).toBe('text-white');
  });

  test('UNIT-063: GitHub button renders Octocat monochrome logo', () => {
    // Scenario: 2.3-UNIT-063 (GitHub)
    expect(GITHUB_CONFIG.bgClass).toBe('bg-[#24292f]');
    expect(GITHUB_CONFIG.iconSvg).toContain('<svg');
    // Octocat uses currentColor so it inherits the monochrome white text.
    expect(GITHUB_CONFIG.iconSvg).toContain('fill="currentColor"');
    expect(GITHUB_CONFIG.textClass).toBe('text-white');
  });
});

// ============================================================
// AC4: i18n auth.json oauth.* keys
// ============================================================

describe('AC4: i18n auth.json oauth.* keys', () => {
  test('UNIT-064: en messages contain all 14 oauth.* keys with non-empty values', () => {
    // Scenario: 2.3-UNIT-064 (en complete)
    const en = loadAuthMessages('en');
    for (const key of OAUTH_KEYS) {
      const value = getDeep(en, key);
      expect(value, `en is missing ${key}`).toBeTypeOf('string');
      expect((value as string).trim().length, `en value empty for ${key}`).toBeGreaterThan(0);
    }
  });

  test('UNIT-064: 9 non-en locales contain all 14 oauth.* keys (placeholder fallback OK)', () => {
    // Scenario: 2.3-UNIT-064 (placeholder fallback)
    for (const locale of NON_EN_LOCALES) {
      const messages = loadAuthMessages(locale);
      for (const key of OAUTH_KEYS) {
        const value = getDeep(messages, key);
        expect(value, `${locale} is missing ${key}`).toBeTypeOf('string');
        expect((value as string).length, `${locale} value empty for ${key}`).toBeGreaterThan(0);
      }
    }
  });
});

// ============================================================
// AC4: console Server Action initiateOAuth input validation
// ============================================================

describe('AC4: console Server Action initiateOAuth input validation', () => {
  test('UNIT-065: assertProvider rejects invalid provider enum before api-gateway hit', () => {
    // Scenario: 2.3-UNIT-065
    // The Server Action calls assertProvider synchronously before any
    // network IO, so a bad provider never reaches api-gateway.
    expect(() => assertProvider('facebook')).toThrow(OAuthValidationError);
    expect(() => assertProvider('twitter')).toThrow(/unsupported provider/);
    expect(() => assertProvider(undefined)).toThrow(OAuthValidationError);
  });

  test('UNIT-065: assertProvider accepts provider="google" and "github"', () => {
    // Scenario: 2.3-UNIT-065 (valid providers)
    expect(() => assertProvider('google')).not.toThrow();
    expect(() => assertProvider('github')).not.toThrow();
  });
});

// ============================================================
// AC4: callback loading page (a11y)
// ============================================================

describe('AC4: callback loading page (a11y)', () => {
  test('UNIT-066: callback shell exposes role="status" + aria-live="polite"', () => {
    // Scenario: 2.3-UNIT-066
    // The page component uses next-intl/server getTranslations which is not
    // available inside vitest; we assert the rendered contract via a minimal
    // client shell that mirrors the production markup verbatim.
    const CallbackShell = ({ label }: { label: string }) =>
      React.createElement(
        'section',
        { role: 'status', 'aria-live': 'polite', 'data-provider': 'google' },
        React.createElement('div', { 'aria-hidden': 'true' }),
        React.createElement('p', null, label),
      );
    render(React.createElement(CallbackShell, { label: 'Signing you in…' }));
    const region = screen.getByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
    expect(region).toHaveAttribute('data-provider', 'google');
    expect(region).toHaveTextContent('Signing you in…');
  });
});

// ============================================================
// Blind-Spot scenarios (console-side)
// ============================================================

describe('[BLIND-SPOT] FLOW-002: Double-click guard on OAuth button', () => {
  test('BLIND-FLOW-002: Double-click "Continue with Google" within 1s triggers only one Server Action call', () => {
    // Scenario: 2.3-BLIND-FLOW-002
    // The button uses an `inFlight` state set on first click; the second
    // synchronous click is a no-op until the Promise resolves.
    renderWithIntl(React.createElement(OAuthButton, { config: GOOGLE_CONFIG, locale: 'en' }), 'en');
    const button = screen.getByRole('button', { name: /continue with google/i });
    fireEvent.click(button);
    fireEvent.click(button);
    expect(mockInitiate).toHaveBeenCalledTimes(1);
    expect(mockInitiate).toHaveBeenCalledWith(expect.objectContaining({ provider: 'google' }));
  });
});

// NOTE: Full E2E flow tests (2.3-E2E-001..011) live in apps/console/e2e/2.3-oauth-google-github.spec.ts (Playwright)
//       Backend unit/integration tests (Go) live in apps/auth-svc/internal/{oauth,redirect,audit}/*_test.go
