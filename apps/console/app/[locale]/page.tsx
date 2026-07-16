import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';
import { isLocale } from '@/i18n/config';
import { notFound } from 'next/navigation';

interface PageProps {
  params: { locale: string };
}

export default async function Page({ params: { locale } }: PageProps) {
  if (!isLocale(locale)) {
    notFound();
  }
  unstable_setRequestLocale(locale);
  const t = await getTranslations('common');

  // 内容恒限宽居中 + 左对齐编辑式版式(design-system layout.container / alignment)。
  // 刻意不套 <PageShell>:2.1-UNIT-086 断言本文件里存在字面量 <h1>{t('demo.title')}</h1>,
  // 故手写与 PageShell 同构的外壳(mx-auto / max-w / px-6 py-10 lg:px-8)。
  return (
    <section className="mx-auto max-w-prose-page px-6 py-10 lg:px-8">
      <header className="mb-6">
        <h1 className="text-h1">{t('demo.title')}</h1>
        <p className="mt-1 text-small text-ink-secondary">{t('demo.currentLocale', { locale })}</p>
      </header>
      {/* 数值是仪表读数 → tabular(铁律1) */}
      <p className="tabular text-metric text-ink">{t('demo.keysCount', { count: 19 })}</p>
    </section>
  );
}
