import type { AbstractIntlMessages } from 'next-intl';
import type { Locale } from './config';
import { NAMESPACES } from './namespaces';

/**
 * Story 10.1 (BR-10.1.1) — load EVERY namespace for a locale. Extracted from
 * request.ts so the wiring is unit-testable WITHOUT next-intl's getRequestConfig
 * (which throws outside a Server Component / request scope).
 *
 * The `../messages/${locale}/${ns}.json` dynamic segments are statically
 * analyzable by the bundler → one chunk per file, so only the requested locale's
 * namespaces ship in its chunk (OQ-10.1-1 / 250KB budget). A missing/malformed
 * namespace rejects this Promise.all and surfaces the error (fail-safe, no silent
 * swallow — 10.1-BLIND-ERROR-001).
 */
export async function loadMessages(locale: Locale): Promise<AbstractIntlMessages> {
  const entries = await Promise.all(
    NAMESPACES.map(
      async (ns) => [ns, (await import(`../messages/${locale}/${ns}.json`)).default] as const,
    ),
  );
  return Object.fromEntries(entries) as AbstractIntlMessages;
}
