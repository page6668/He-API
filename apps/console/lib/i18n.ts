import { locales, defaultLocale, fallbackMap, isLocale, COOKIE_NAME, type Locale } from '@/i18n/config';

const RTL_LOCALES: ReadonlySet<string> = new Set(['ar', 'fa', 'he', 'ur']);

export function isRtlLocale(locale: string): boolean {
  return RTL_LOCALES.has(locale);
}

export interface CookieOptions {
  name: typeof COOKIE_NAME;
  path: '/';
  maxAge: number;
  sameSite: 'lax';
  httpOnly: false;
  secure: boolean;
  domain: string | undefined;
}

export type Env = 'production' | 'staging' | 'development';

export function buildLocaleCookieOptions(env: Env): CookieOptions {
  return {
    name: COOKIE_NAME,
    path: '/',
    maxAge: 31536000,
    sameSite: 'lax',
    httpOnly: false,
    secure: env !== 'development',
    domain:
      env === 'production'
        ? '.he-api.com'
        : env === 'staging'
          ? '.staging.he-api.com'
          : undefined,
  };
}

interface AcceptLanguageEntry {
  tag: string;
  q: number;
}

function parseAcceptLanguage(header: string | null | undefined): AcceptLanguageEntry[] {
  if (!header) return [];
  const entries: AcceptLanguageEntry[] = [];
  for (const part of header.split(',')) {
    const trimmed = part.trim();
    if (!trimmed) continue;
    const [tagRaw, ...params] = trimmed.split(';');
    if (!tagRaw) continue;
    const tag = tagRaw.trim();
    if (!tag || tag === '*') continue;
    let q = 1;
    for (const p of params) {
      const m = /^\s*q\s*=\s*([0-9.]+)\s*$/i.exec(p);
      if (m && m[1] !== undefined) {
        const parsed = Number.parseFloat(m[1]);
        if (Number.isFinite(parsed)) q = parsed;
      }
    }
    entries.push({ tag, q });
  }
  entries.sort((a, b) => b.q - a.q);
  return entries;
}

function matchAgainstLocales(tag: string): Locale | null {
  if (isLocale(tag)) return tag;
  const fb = fallbackMap[tag];
  if (fb) return fb;
  // Try language-only fallback: "fr-FR" → "fr"
  const idx = tag.indexOf('-');
  if (idx > 0) {
    const base = tag.slice(0, idx);
    if (isLocale(base)) return base;
    const fbBase = fallbackMap[base];
    if (fbBase) return fbBase;
  }
  return null;
}

export interface ResolveLocaleInput {
  cookieValue?: string | null;
  acceptLanguage?: string | null;
}

export function resolveLocale({ cookieValue, acceptLanguage }: ResolveLocaleInput): Locale {
  if (cookieValue && cookieValue.length <= 16) {
    if (isLocale(cookieValue)) return cookieValue;
    const fb = fallbackMap[cookieValue];
    if (fb) return fb;
  }
  const entries = parseAcceptLanguage(acceptLanguage);
  for (const { tag } of entries) {
    const matched = matchAgainstLocales(tag);
    if (matched) return matched;
  }
  return defaultLocale;
}

export { locales, defaultLocale, fallbackMap, isLocale, COOKIE_NAME };
export type { Locale };
