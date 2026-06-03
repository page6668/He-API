/**
 * Story 5.4 — cap-monitor i18n wiring stub.
 *
 * Story 5.4 ships the server-side cap-threshold email path (gateway →
 * notification-svc); the Console cap-display UI lands in Story 5.5. This stub
 * asserts the NEW `account.keys.cap.email.*` i18n namespace is wired across all
 * 10 MVP locales (subject + description for the warning + tripped emails) so
 * Story 5.5 can build the display surface against a complete key set.
 *
 * Strategy: static JSON-parse of the message catalogues (no runtime / Playwright).
 */

import { describe, expect, it } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const LOCALES = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;
const CAP_LEAVES = [
  ['warning', 'subject'],
  ['warning', 'description'],
  ['tripped', 'subject'],
  ['tripped', 'description'],
] as const;

function loadAccount(locale: string): any {
  const path = resolve(__dirname, '..', 'messages', locale, 'account.json');
  expect(existsSync(path), `${path} must exist`).toBe(true);
  return JSON.parse(readFileSync(path, 'utf-8'));
}

describe('Story 5.4 — account.keys.cap.email.* i18n wiring', () => {
  it('every locale defines the full cap.email key set', () => {
    for (const locale of LOCALES) {
      const email = loadAccount(locale)?.keys?.cap?.email;
      expect(email, `${locale}: keys.cap.email missing`).toBeDefined();
      for (const [kind, field] of CAP_LEAVES) {
        const value = email?.[kind]?.[field];
        expect(typeof value, `${locale}: keys.cap.email.${kind}.${field}`).toBe('string');
        expect(value.length, `${locale}: keys.cap.email.${kind}.${field} non-empty`).toBeGreaterThan(0);
      }
    }
  });

  it('en + zh-CN are fully translated (no [en-pending] marker)', () => {
    for (const locale of ['en', 'zh-CN']) {
      const email = loadAccount(locale).keys.cap.email;
      for (const [kind, field] of CAP_LEAVES) {
        expect(email[kind][field]).not.toContain('[en-pending]');
      }
    }
  });

  it('the 8 deferred locales carry the [en-pending] marker (Story-2.1 m-1 cascade)', () => {
    for (const locale of ['ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar']) {
      const email = loadAccount(locale).keys.cap.email;
      expect(email.warning.subject).toContain('[en-pending]');
    }
  });
});
