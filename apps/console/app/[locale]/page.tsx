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

  return (
    <section className="mx-auto max-w-2xl px-6 py-12 text-center">
      <h1 className="text-3xl font-bold">{t('demo.title')}</h1>
      <p className="mt-4 text-muted-foreground">{t('demo.currentLocale', { locale })}</p>
      <p className="mt-2 text-sm">{t('demo.keysCount', { count: 19 })}</p>
    </section>
  );
}
