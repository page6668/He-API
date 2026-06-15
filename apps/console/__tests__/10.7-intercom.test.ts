// Story 10.7 AC2 — Intercom customer-support integration tests.
//
// Designed against the Architect-ratified OQ-2 ZERO-PII hard contract + the QA
// 47-scenario test design (docs/qa/assessments/10.7-test-design-20260616.md):
//   UNIT-010 zero-PII boot payload (property)     · UNIT-011 secret server-only
//   UNIT-012 HMAC seam dormant by default          · UNIT-013 PRC region-gate
//   UNIT-014 HMAC correctness (golden vector)      · UNIT-015 he_request_id regex
//   UNIT-016 language_override mapping             · UNIT-018 ar locale
//   INT-010  BFF /api/intercom-hash isolation      · BOUNDARY-010 missing app_id
//   ERROR-011 HMAC-disabled route degrade
//
// Pure-logic + node:crypto + route-handler unit tests (no real network / SaaS).

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { HE_REQUEST_ID_REGEX } from '@/lib/request-id';
import {
  FORBIDDEN_INTERCOM_KEYS,
  buildBootPayload,
  buildSupportSessionUpdate,
} from '@/lib/intercom/boot-payload';
import { toLanguageOverride } from '@/lib/intercom/config';
import { isPrcRegion, shouldBootIntercom } from '@/lib/intercom/region';
import { computeUserHash } from '@/lib/intercom/hmac';
import { openSupportWithRequestId } from '@/lib/intercom/messenger';

// ---------------------------------------------------------------------------
// AC2 · UNIT-010 — ZERO-PII boot payload (HARD, property test)
// ---------------------------------------------------------------------------
describe('10.7-UNIT-010 zero-PII boot payload (property)', () => {
  // A representative spread of session/user shapes an unsafe impl might leak.
  const userShapes: Array<Record<string, unknown>> = [
    { email: 'a@b.com', name: 'Alice', user_id: 'usr_1', ip: '1.2.3.4' },
    { first_name: 'Bob', last_name: 'Lee', phone: '+8613800000000', company: 'ACME' },
    { prompt: 'secret prompt', content: 'message body', api_key: 'sk-live-xxx' },
    {},
  ];

  it('never serializes any PII key — only app_id + language_override', () => {
    for (const shape of userShapes) {
      for (const locale of ['en', 'zh-CN', 'ar', 'xx']) {
        // The builder takes ONLY appId + locale; user shape must be unreachable.
        const payload = buildBootPayload({ appId: 'app_pub_123', locale });
        const serialized = JSON.stringify(payload);
        for (const forbidden of FORBIDDEN_INTERCOM_KEYS) {
          expect(serialized).not.toContain(`"${forbidden}"`);
        }
        // sanity: it carries exactly the two non-PII keys
        expect(Object.keys(payload).sort()).toEqual(['app_id', 'language_override']);
        // the user shape's values must not have leaked through
        void shape;
        expect(serialized).not.toContain('a@b.com');
        expect(serialized).not.toContain('secret prompt');
        expect(serialized).not.toContain('sk-live-xxx');
      }
    }
  });

  it('support-session update carries ONLY the non-PII he_request_id handle', () => {
    const update = buildSupportSessionUpdate('req_a1b2c3d4e5f6');
    expect(update).toEqual({ he_request_id: 'req_a1b2c3d4e5f6' });
    const serialized = JSON.stringify(update);
    for (const forbidden of FORBIDDEN_INTERCOM_KEYS) {
      expect(serialized).not.toContain(`"${forbidden}"`);
    }
  });
});

// ---------------------------------------------------------------------------
// AC2 · UNIT-011 — INTERCOM_SECRET is server-only (static source scan)
// ---------------------------------------------------------------------------
describe('10.7-UNIT-011 INTERCOM_SECRET never reaches the client bundle', () => {
  const root = resolve(__dirname, '..');

  it('the client Messenger component never references the secret or server-only modules', () => {
    const src = readFileSync(resolve(root, 'components/business/IntercomMessenger.tsx'), 'utf8');
    expect(src).toContain("'use client'");
    expect(src).not.toContain('INTERCOM_SECRET');
    expect(src).not.toMatch(/intercom\/hmac/);
    expect(src).not.toMatch(/intercom\/identity/);
  });

  it('the secret is never exposed through a NEXT_PUBLIC_* variable anywhere', () => {
    // NEXT_PUBLIC_* is the ONLY env surface inlined into the client bundle.
    for (const rel of [
      'lib/intercom/identity.ts',
      'lib/intercom/hmac.ts',
      'app/api/intercom-hash/route.ts',
      'components/business/IntercomMessenger.tsx',
    ]) {
      const src = readFileSync(resolve(root, rel), 'utf8');
      expect(src).not.toMatch(/NEXT_PUBLIC_[A-Z_]*SECRET/);
      expect(src).not.toContain('NEXT_PUBLIC_INTERCOM_SECRET');
    }
  });
});

// ---------------------------------------------------------------------------
// AC2 · UNIT-013 / BOUNDARY-010 — boot gating
// ---------------------------------------------------------------------------
describe('10.7-UNIT-013 PRC region-gate + BOUNDARY-010 missing app_id', () => {
  it('PRC-region request does NOT boot Intercom (no snippet egress)', () => {
    expect(isPrcRegion('CN')).toBe(true);
    expect(isPrcRegion('cn')).toBe(true);
    expect(isPrcRegion(' CN ')).toBe(true);
    expect(shouldBootIntercom({ appId: 'app_123', countryCode: 'CN' })).toBe(false);
  });

  it('non-PRC region with a valid app_id boots', () => {
    expect(isPrcRegion('US')).toBe(false);
    expect(isPrcRegion(null)).toBe(false);
    expect(shouldBootIntercom({ appId: 'app_123', countryCode: 'US' })).toBe(true);
    expect(shouldBootIntercom({ appId: 'app_123', countryCode: null })).toBe(true);
  });

  it('missing/empty app_id never boots (graceful, no crash)', () => {
    expect(shouldBootIntercom({ appId: '', countryCode: 'US' })).toBe(false);
    expect(shouldBootIntercom({ appId: undefined, countryCode: 'US' })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// AC2 · UNIT-016 / UNIT-018 — language_override mapping
// ---------------------------------------------------------------------------
describe('10.7-UNIT-016 language_override maps the 10 console locales', () => {
  it.each([
    ['en', 'en'],
    ['zh-CN', 'zh-CN'],
    ['ja', 'ja'],
    ['ko', 'ko'],
    ['es', 'es'],
    ['fr', 'fr'],
    ['de', 'de'],
    ['pt', 'pt'],
    ['ru', 'ru'],
    ['ar', 'ar'], // UNIT-018 — RTL rendering delegated to Intercom
  ])('locale %s → language_override %s', (locale, expected) => {
    expect(toLanguageOverride(locale)).toBe(expected);
  });

  it('unknown locale falls back to en', () => {
    expect(toLanguageOverride('xx')).toBe('en');
    expect(toLanguageOverride('')).toBe('en');
  });
});

// ---------------------------------------------------------------------------
// AC2 · UNIT-015 — he_request_id handle validation (reuse §11.5 regex, L-2)
// ---------------------------------------------------------------------------
describe('10.7-UNIT-015 he_request_id handle regex (shared §11.5 constant)', () => {
  it('matches the exact §11.5 shape', () => {
    expect(HE_REQUEST_ID_REGEX.source).toBe('^req_[a-f0-9]{12}$');
    expect(HE_REQUEST_ID_REGEX.test('req_a1b2c3d4e5f6')).toBe(true);
  });

  it('malformed handle is NOT attached and does not block the session', () => {
    expect(buildSupportSessionUpdate('nope')).toBeNull();
    expect(buildSupportSessionUpdate('req_XYZ')).toBeNull();
    expect(buildSupportSessionUpdate('req_a1b2c3')).toBeNull();
    expect(buildSupportSessionUpdate('REQ_a1b2c3d4e5f6')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// AC2 · UNIT-014 — HMAC correctness (built-but-dormant capability)
// ---------------------------------------------------------------------------
describe('10.7-UNIT-014 HMAC-SHA256 over the immutable id', () => {
  it('matches a precomputed golden vector and is byte-stable', () => {
    const secret = 'golden-test-secret-do-not-use';
    const userId = 'usr_a1b2c3d4e5f6';
    const golden = '2513d1134be80d5a0d2b1498f19c2155f2a502cdeeebc9095de59130fa77c5ec';
    expect(computeUserHash(secret, userId)).toBe(golden);
    expect(computeUserHash(secret, userId)).toBe(computeUserHash(secret, userId));
  });

  it('differs for a different (immutable) id', () => {
    const secret = 'golden-test-secret-do-not-use';
    expect(computeUserHash(secret, 'usr_aaaaaaaaaaaa')).not.toBe(
      computeUserHash(secret, 'usr_bbbbbbbbbbbb'),
    );
  });
});

// ---------------------------------------------------------------------------
// AC2 · INT-012 — one-click he_request_id into the support session
// ---------------------------------------------------------------------------
describe('10.7-INT-012 openSupportWithRequestId brings the handle into the session', () => {
  interface IntercomGlobal extends Window {
    Intercom?: ((...args: unknown[]) => void) & { calls?: unknown[][] };
  }
  afterEach(() => {
    delete (window as IntercomGlobal).Intercom;
  });

  it('updates the session with ONLY he_request_id when the handle is valid', () => {
    const calls: unknown[][] = [];
    (window as IntercomGlobal).Intercom = (...a: unknown[]) => calls.push(a);
    expect(openSupportWithRequestId('req_a1b2c3d4e5f6')).toBe(true);
    const update = calls.find((c) => c[0] === 'update');
    expect(update?.[1]).toEqual({ he_request_id: 'req_a1b2c3d4e5f6' });
  });

  it('does nothing (returns false) for a malformed handle — session not blocked', () => {
    const calls: unknown[][] = [];
    (window as IntercomGlobal).Intercom = (...a: unknown[]) => calls.push(a);
    expect(openSupportWithRequestId('bogus')).toBe(false);
    expect(calls.length).toBe(0);
  });

  it('degrades silently when the Messenger is not booted (no crash)', () => {
    delete (window as IntercomGlobal).Intercom;
    expect(() => openSupportWithRequestId('req_a1b2c3d4e5f6')).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// AC2 · INT-010 / UNIT-012 / ERROR-011 — BFF /api/intercom-hash route
// ---------------------------------------------------------------------------
describe('10.7-INT-010 /api/intercom-hash BFF route (dormant by default)', () => {
  const ORIGINAL = { ...process.env };
  afterEach(() => {
    process.env = { ...ORIGINAL };
    vi.resetModules();
  });

  async function loadRoute() {
    vi.resetModules();
    return import('@/app/api/intercom-hash/route');
  }

  it('UNIT-012 — DORMANT by default: route is unreachable (404) with no flag set', async () => {
    delete process.env.INTERCOM_IDENTITY_VERIFICATION;
    delete process.env.INTERCOM_SECRET;
    const { POST } = await loadRoute();
    const res = await POST(new Request('http://x/api/intercom-hash', {
      method: 'POST',
      body: JSON.stringify({ user_id: 'usr_a1b2c3d4e5f6' }),
    }));
    expect(res.status).toBe(404);
  });

  it('stays dormant when the flag is on but no secret is provisioned', async () => {
    process.env.INTERCOM_IDENTITY_VERIFICATION = 'on';
    delete process.env.INTERCOM_SECRET;
    const { POST } = await loadRoute();
    const res = await POST(new Request('http://x/api/intercom-hash', {
      method: 'POST',
      body: JSON.stringify({ user_id: 'usr_a1b2c3d4e5f6' }),
    }));
    expect(res.status).toBe(404);
  });

  it('when explicitly enabled: returns ONLY {user_hash} and never echoes the secret', async () => {
    process.env.INTERCOM_IDENTITY_VERIFICATION = 'on';
    process.env.INTERCOM_SECRET = 'golden-test-secret-do-not-use';
    const { POST } = await loadRoute();
    const res = await POST(new Request('http://x/api/intercom-hash', {
      method: 'POST',
      body: JSON.stringify({ user_id: 'usr_a1b2c3d4e5f6' }),
    }));
    expect(res.status).toBe(200);
    const body = await res.json();
    expect(Object.keys(body)).toEqual(['user_hash']);
    expect(body.user_hash).toBe(computeUserHash('golden-test-secret-do-not-use', 'usr_a1b2c3d4e5f6'));
    expect(JSON.stringify(body)).not.toContain('golden-test-secret-do-not-use');
  });

  it('enabled but malformed body → 400 (never leaks secret)', async () => {
    process.env.INTERCOM_IDENTITY_VERIFICATION = 'on';
    process.env.INTERCOM_SECRET = 'golden-test-secret-do-not-use';
    const { POST } = await loadRoute();
    const res = await POST(new Request('http://x/api/intercom-hash', { method: 'POST', body: '{bad' }));
    expect(res.status).toBe(400);
    expect(JSON.stringify(await res.json())).not.toContain('golden-test-secret-do-not-use');
  });
});
