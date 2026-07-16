/**
 * Story 10.6 — `/{locale}/benchmark` (AC2). PUBLIC marketing page (no login),
 * SEO-indexable + cacheable (revalidate), under the 4.7 (marketing) route group.
 *
 * Renders the curated benchmark (Zod-validated, graceful fallback banner on shape
 * drift), the 3-metric Recharts visualization, mandatory provenance (Methodology +
 * Last-updated + sources + disclaimer), and an "Open A/B in Playground" jump.
 * He-model prices render from the catalogue SoT (never hardcoded in the seed).
 */
import type { Metadata } from 'next';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { BenchmarkChart } from '@/components/business/BenchmarkChart';
import { LtrText } from '@/components/business/LtrText';
import { Notice, Panel, PageShell } from '@/components/ui/kit';
import { loadBenchmarkData } from '@/lib/api/benchmark';

// Public + cacheable (区别网关 no-store) — sourced curated data refreshes slowly.
export const revalidate = 3600;

interface PageProps {
  params: { locale: string };
}

export async function generateMetadata({ params: { locale } }: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'benchmark' });
  const title = t('page.title');
  const description = t('page.description');
  const canonical = `https://he-api.com/${locale}/benchmark`;
  return {
    title,
    description,
    alternates: { canonical },
    openGraph: { title, description, url: canonical, type: 'website' },
  };
}

export default async function BenchmarkPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'benchmark' });
  const result = loadBenchmarkData();

  // 公开页恒限宽居中(prose-page = 1120px)—— 禁止内容裸贴视口(design-system layout.container)
  return (
    <PageShell width="prose-page" title={t('page.heading')} subtitle={t('page.subheading')}>
      {!result.ok ? (
        // 安静的行内提示条,不是满宽色底 banner
        <Notice tone="warning" role="alert">
          <span data-testid="benchmark-fallback-banner">{t('error.unavailable')}</span>
        </Notice>
      ) : (
        <>
          <Panel>
            <BenchmarkChart data={result.data} playgroundHref={`/${locale}/playground`} />
          </Panel>

          {/* 出处 = 读数的一部分:日期走 tabular */}
          <p className="mt-4 text-small text-ink-secondary">
            {t('lastUpdated')}:{' '}
            <span className="tabular">
              <LtrText>{result.data.lastUpdated}</LtrText>
            </span>
          </p>

          <details className="mt-4" data-testid="benchmark-methodology">
            <summary className="cursor-pointer text-small font-medium text-ink transition-colors duration-state ease-he hover:text-ink-secondary">
              {t('methodology.heading')}
            </summary>
            <p className="mt-2 text-small text-ink-secondary">{t('methodology.body')}</p>
            <ul className="mt-2 list-disc ps-6 text-small text-ink-secondary">
              {result.data.methodologySources.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ul>
          </details>

          {/* 中文禁 italic(design-system line_height)—— 免责声明降为 muted 小字即可 */}
          <p className="mt-4 text-label text-ink-muted" data-testid="benchmark-disclaimer">
            {t('disclaimer')}
          </p>
        </>
      )}
    </PageShell>
  );
}
