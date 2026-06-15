/**
 * Story 9.1 AC3 — `/{locale}/dashboard` — the default authenticated landing
 * (front-end-spec §2.1 sitemap). Server Component. SSR-fetches the usage summary
 * via the getUsageSummary() BFF action behind a Suspense boundary (skeleton while
 * pending), then renders <UsageStatCards>. 401 → redirect to /signin (BR-RD-3);
 * envelope error → ErrorBanner + Retry. Below the card band sits the Story 9.1b
 * <UsageChart> trend chart (client-fetched; its loading/error degrade in place so
 * a /series outage never takes down the card band — BR-CH-4).
 */
import type { Metadata } from 'next';
import { Suspense } from 'react';
import { redirect } from 'next/navigation';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { UsageStatCards } from '@/components/business/UsageStatCards';
import { UsageStatCardsSkeleton } from '@/components/business/UsageStatCardsSkeleton';
import { UsageChart } from '@/components/business/UsageChart';
import { getUsageSummary } from './_actions/get-usage-summary';

interface PageProps {
  params: { locale: string };
}

export async function generateMetadata({ params: { locale } }: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'dashboard' });
  return {
    title: t('page.title'),
    description: t('page.description'),
  };
}

export default async function DashboardPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'dashboard' });

  return (
    <main aria-labelledby="dashboard-heading" className="space-y-6">
      <h1 id="dashboard-heading" className="text-2xl font-semibold">
        {t('page.heading')}
      </h1>
      <Suspense fallback={<UsageStatCardsSkeleton label={t('loading.label')} />}>
        <DashboardContent locale={locale} />
      </Suspense>
      <UsageChart locale={locale} />
    </main>
  );
}

async function DashboardContent({ locale }: { locale: string }) {
  const t = await getTranslations({ locale, namespace: 'dashboard' });
  const tRoot = await getTranslations({ locale });
  const result = await getUsageSummary();

  if (!result.ok && result.unauthorized) {
    redirect(`/${locale}/signin?return_to=${encodeURIComponent(`/${locale}/dashboard`)}`);
  }

  if (result.ok) {
    return <UsageStatCards summary={result.summary} locale={locale} />;
  }

  return (
    <div
      role="alert"
      className="space-y-2 rounded border border-red-300 bg-red-50 p-4 text-sm text-red-900"
    >
      <p>{tRoot(result.error.code)}</p>
      <a
        href={`/${locale}/dashboard`}
        className="inline-block rounded bg-red-600 px-3 py-1.5 text-white hover:bg-red-700"
      >
        {t('errors.retry')}
      </a>
    </div>
  );
}
