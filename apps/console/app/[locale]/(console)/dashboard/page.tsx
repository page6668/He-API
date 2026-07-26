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

import { PageShell, linkCls } from '@/components/ui/kit';
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
    <main aria-labelledby="dashboard-heading">
      <PageShell
        title={t('page.heading')}
        titleId="dashboard-heading"
        subtitle={t('page.description')}
        width="console"
      >
        <div className="space-y-6">
          <Suspense fallback={<UsageStatCardsSkeleton label={t('loading.label')} />}>
            <DashboardContent locale={locale} />
          </Suspense>
          <UsageChart locale={locale} />
        </div>
      </PageShell>
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

  // 安静的一行说明 + 靛青文字链重试(design-system.md key_page_direction.dashboard:
  // 「空/错状态用一行安静说明 + 文字链重试,不要满宽红底 banner」;链接走 kit linkCls,
  // indigo 管「信息」)。role="alert" 保留原 a11y 语义。
  return (
    <p role="alert" className="text-small text-ink-secondary">
      {tRoot(result.error.code)}{' '}
      <a href={`/${locale}/dashboard`} className={linkCls}>
        {t('errors.retry')}
      </a>
    </p>
  );
}
