import { useTranslations } from 'next-intl';

export default function NotFound() {
  const t = useTranslations('common');
  return (
    <section className="mx-auto max-w-2xl px-6 py-24 text-center">
      <h1 className="text-3xl font-bold">{t('errors.notFound.title')}</h1>
      <p className="mt-4 text-muted-foreground">{t('errors.notFound.description')}</p>
      <a href="/" className="mt-6 inline-block underline">
        {t('errors.notFound.backHome')}
      </a>
    </section>
  );
}
