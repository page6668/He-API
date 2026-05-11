'use server';

import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { locales, isLocale, COOKIE_NAME, type Locale } from '@/i18n/config';
import { buildLocaleCookieOptions, type Env } from '@/lib/i18n';

function currentEnv(): Env {
  const envVar = process.env.NEXT_PUBLIC_DEPLOY_ENV ?? process.env.NODE_ENV ?? 'development';
  if (envVar === 'production') return 'production';
  if (envVar === 'staging') return 'staging';
  return 'development';
}

function swapLocaleSegment(currentPath: string, currentLocale: string, newLocale: string): string {
  const trimmed = currentPath.startsWith('/') ? currentPath.slice(1) : currentPath;
  const segments = trimmed.split('/');
  if (segments[0] === currentLocale) {
    segments[0] = newLocale;
  } else {
    segments.unshift(newLocale);
  }
  return '/' + segments.join('/');
}

export async function setLocale(newLocale: Locale, currentLocale: string, currentPath: string): Promise<never> {
  if (!isLocale(newLocale)) {
    throw new Error(`Invalid locale: ${String(newLocale)}`);
  }
  const opts = buildLocaleCookieOptions(currentEnv());
  cookies().set(COOKIE_NAME, newLocale, {
    path: opts.path,
    maxAge: opts.maxAge,
    sameSite: opts.sameSite,
    httpOnly: opts.httpOnly,
    secure: opts.secure,
    ...(opts.domain ? { domain: opts.domain } : {}),
  });
  const target = swapLocaleSegment(currentPath || '/', currentLocale, newLocale);
  redirect(target);
}

export { locales };
