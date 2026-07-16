/**
 * Story 5.5 AC2 — one-time plaintext display sub-page (T2.3).
 *
 * Server Component. Reads the plaintext from the URL searchParams (Q-U1), guards
 * its shape (BR-PD-4 — deep-links without a well-formed plaintext render 404),
 * and renders <ApiKeyDisplay> once. force-dynamic + revalidate=0 defeat any
 * segment cache (BR-PD-2); metadata sets noindex + no-referrer (BR-PD-5/6).
 *
 * NOTE: App Router has no per-segment `export const headers`; the Cache-Control
 * hardening for this path (BR-PD-7) is applied in middleware.ts.
 */
import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { PageShell } from '@/components/ui/kit';
import { ApiKeyDisplay } from '@/components/business/ApiKeyDisplay';
import { PLAINTEXT_RE } from '@/lib/api/me-keys';

export const dynamic = 'force-dynamic';
export const revalidate = 0;

export const metadata: Metadata = {
  robots: { index: false, follow: false, nocache: true },
  referrer: 'no-referrer',
};

interface PageProps {
  params: { locale: string; api_key_id: string };
  searchParams: { plaintext?: string };
}

export default async function CreatedKeyPage({
  params: { locale, api_key_id },
  searchParams,
}: PageProps) {
  unstable_setRequestLocale(locale);

  const plaintext = searchParams.plaintext;
  if (!plaintext || !PLAINTEXT_RE.test(plaintext)) {
    notFound();
  }

  // 命名空间沿用 ApiKeyDisplay 既有的 'account.keys'(account.json 内的子路径,
  // 非顶层命名空间提升 —— 与 client 组件的 useTranslations('account.keys') 一致)。
  const t = await getTranslations({ locale, namespace: 'account.keys' });

  return (
    // 内容恒有最大宽度并居中 —— 治「撑满整屏」(design-system.md layout.container)
    <main aria-labelledby="created-key-heading">
      <PageShell title={t('created.title')} titleId="created-key-heading" width="console">
        <ApiKeyDisplay plaintext={plaintext} apiKeyId={api_key_id} locale={locale} />
      </PageShell>
    </main>
  );
}
