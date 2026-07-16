/**
 * Story 5.5 AC1 — `/{locale}/keys` list page (T1.1).
 *
 * Server Component. SSR-fetches the keys via the listMyKeys() BFF action and
 * the model catalogue via fetchPublicModels() (passed to the configure drawer —
 * the gateway is server-only, so the catalogue is sourced here rather than by a
 * client cross-origin fetch). Renders the page chrome + <KeysPanel> (client
 * orchestrator) on success, or an error banner with a Retry link.
 *
 * Note (BR-L-3): unauthenticated access (missing he_access cookie) is handled
 * by the (console) layout, which redirects to /signin. An expired-but-present
 * cookie surfaces here as the error banner (the shipped Story-5.1 listMyKeys
 * action collapses all non-200s to a generic error code).
 */
import type { Metadata } from 'next';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { PageShell, Notice } from '@/components/ui/kit';
import { KeysPanel } from '@/components/business/KeysPanel';
import { fetchPublicModels } from '@/lib/api/public-models';
import { listMyKeys } from './_actions/list-keys';

interface PageProps {
  params: { locale: string };
}

export async function generateMetadata({ params: { locale } }: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'account.keys' });
  return {
    title: t('page.title'),
    description: t('page.description'),
  };
}

export default async function KeysPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'account.keys' });
  const tRoot = await getTranslations({ locale });

  const [result, models] = await Promise.all([listMyKeys(), fetchPublicModels()]);
  const modelIds = models.data.map((m) => m.id);

  return (
    <main aria-labelledby="keys-page-heading">
      <PageShell
        title={t('page.title')}
        titleId="keys-page-heading"
        subtitle={t('page.description')}
        width="console"
      >
        {result.ok ? (
          <KeysPanel keys={result.response.data} locale={locale} availableModels={modelIds} />
        ) : (
          <Notice tone="error" role="alert">
            <span>{tRoot(result.error.code)}</span>{' '}
            <a
              href={`/${locale}/keys`}
              className="font-medium underline underline-offset-2 transition-colors duration-state ease-he hover:text-crimson/80"
            >
              {t('banner.retry')}
            </a>
          </Notice>
        )}
      </PageShell>
    </main>
  );
}
