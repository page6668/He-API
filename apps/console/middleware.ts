import createMiddleware from 'next-intl/middleware';
import { locales, defaultLocale } from '@/i18n/config';

// Cookie name MUST stay literal here (Story 2.1 Architect Q4 ruling: custom name
// 'he_locale', not the framework default). Reflected in
// lib/i18n.ts:COOKIE_NAME export for Server Action use.
export default createMiddleware({
  locales: [...locales],
  defaultLocale,
  localePrefix: 'always',
  localeDetection: true,
  localeCookie: {
    name: 'he_locale',
  },
});

export const config = {
  matcher: ['/((?!api|_next|_vercel|.*\\..*).*)'],
};
