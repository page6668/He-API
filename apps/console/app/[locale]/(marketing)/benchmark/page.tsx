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

  return (
    <section>
      <h1 className="mb-1 text-2xl font-bold">{t('page.heading')}</h1>
      <p className="mb-6 text-slate-600">{t('page.subheading')}</p>

      {!result.ok ? (
        <div role="alert" data-testid="benchmark-fallback-banner" className="rounded border border-amber-300 bg-amber-50 px-4 py-3 text-amber-900">
          {t('error.unavailable')}
        </div>
      ) : (
        <>
          <BenchmarkChart data={result.data} playgroundHref={`/${locale}/playground`} />

          <p className="mt-4 text-sm text-slate-500">
            {t('lastUpdated')}: <LtrText>{result.data.lastUpdated}</LtrText>
          </p>

          <details className="mt-4" data-testid="benchmark-methodology">
            <summary className="cursor-pointer font-medium">{t('methodology.heading')}</summary>
            <p className="mt-2 text-sm text-slate-600">{t('methodology.body')}</p>
            <ul className="mt-2 list-disc ps-6 text-sm text-slate-600">
              {result.data.methodologySources.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ul>
          </details>

          <p className="mt-4 text-xs italic text-slate-500" data-testid="benchmark-disclaimer">
            {t('disclaimer')}
          </p>
        </>
      )}
    </section>
  );
}
