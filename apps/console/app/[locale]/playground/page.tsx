/**
 * Story 10.6 — `/{locale}/playground` (AC1). Authed power-user surface (the
 * gateway proxy endpoint enforces the JWT cookie; an unauthenticated call → 401).
 * The page is a thin server shell: it pins the request locale + SEO-light metadata
 * and renders the interactive <Playground/> client component.
 *
 * Location per source-tree.md:51 (`apps/console/app/[locale]/playground/`).
 */
import type { Metadata } from 'next';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { Playground } from '@/components/business/Playground';

interface PageProps {
  params: { locale: string };
}

export async function generateMetadata({ params: { locale } }: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'playground' });
  return {
    title: t('page.title'),
    description: t('page.description'),
  };
}

export default function PlaygroundPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  return <Playground locale={locale} />;
}
