// Story 10.7 AC2 — Intercom (customer-support) shared, framework-agnostic config.
//
// Intercom is the customer-support Messenger (tech-stack「客服」row, Story 10.7).
// Architect-ratified posture (OQ-2 / OQ-6): the international, authed console
// face only, with a ZERO-PII boot payload (only the §11.5 he_request_id handle).
//
// This module holds NO secret. `NEXT_PUBLIC_INTERCOM_APP_ID` is a PUBLIC app id
// (not a credential) — it is the only Intercom env the client bundle ever sees.

/** Public (non-secret) Intercom workspace id env var — safe for the client bundle. */
export const INTERCOM_APP_ID_ENV = 'NEXT_PUBLIC_INTERCOM_APP_ID';

// Console 10-locale → Intercom `language_override` map (BR-10.7.11). Intercom
// renders ar RTL itself; the console only passes the locale code.
const LANGUAGE_OVERRIDE: Readonly<Record<string, string>> = {
  en: 'en',
  'zh-CN': 'zh-CN',
  ja: 'ja',
  ko: 'ko',
  es: 'es',
  fr: 'fr',
  de: 'de',
  pt: 'pt',
  ru: 'ru',
  ar: 'ar',
};

const FALLBACK_LANGUAGE = 'en';

/** Map a console locale to Intercom's `language_override`; unknown → `en`. */
export function toLanguageOverride(locale: string | null | undefined): string {
  if (!locale) return FALLBACK_LANGUAGE;
  return LANGUAGE_OVERRIDE[locale] ?? FALLBACK_LANGUAGE;
}
