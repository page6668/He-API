import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale } from '@/i18n/config';

interface CheckInboxPageProps {
  params: { locale: string };
  searchParams: { email?: string };
}

export default async function CheckInboxPage({
  params: { locale },
  searchParams: { email },
}: CheckInboxPageProps) {
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const t = await getTranslations();

  // `email` is the masked form coming from the registerUser redirect. We
  // never trust it as-is because anyone can hand-craft this URL — but
  // displaying it back as-is is fine since we render with React's automatic
  // string escaping and the value is plain text inside a <p>.
  const maskedEmail = typeof email === 'string' && email.length > 0 ? email : '';

  return (
    <section aria-labelledby="check-inbox-title" className="space-y-4 text-center">
      <h1 id="check-inbox-title" className="text-2xl font-semibold">
        {t('signupCheckInbox.title')}
      </h1>
      <p className="text-sm text-neutral-700">
        {t('signupCheckInbox.description', { email: maskedEmail })}
      </p>
      <div className="rounded-md bg-neutral-50 p-4 text-sm text-neutral-600">
        {t('signupCheckInbox.supportNote')}
      </div>
    </section>
  );
}
