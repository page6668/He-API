// Story 2.5 AC1 — Settings → Profile page (Server Component).
//
// Fetches the current profile via the getMyProfile Server Action, then
// renders the ProfileForm (client) with the data + etag for AC2's
// optimistic-concurrency contract.

import { redirect } from 'next/navigation';
import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { getMyProfile } from './_actions/get-my-profile';
import { ProfileForm } from '@/components/business/ProfileForm';

interface ProfilePageProps {
  params: { locale: string };
}

export default async function ProfilePage({ params: { locale } }: ProfilePageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const result = await getMyProfile();

  if (result.kind === 'unauthorized') {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/profile`)}`);
  }
  if (result.kind === 'pending_deletion') {
    // Story 2.7 AC4 BR-4.3 — the recovery page is now live; route straight
    // there (supersedes the interim /signin?error=account_pending_deletion).
    // (The (console) layout guard catches this first, but keep the per-page
    // redirect as defence-in-depth.)
    redirect(`/${resolvedLocale}/account/recovery`);
  }
  if (result.kind === 'error') {
    const t = await getTranslations('account');
    return (
      <section className="space-y-4 py-8">
        <h1 className="text-2xl font-semibold">{(await getTranslations('account'))('profile.title')}</h1>
        <p role="alert" className="text-sm text-red-600">
          {t('profile.errors.load_failed')}
        </p>
      </section>
    );
  }

  return (
    <ProfileForm
      defaults={result.data}
      etag={result.etag}
      currentLocale={resolvedLocale}
    />
  );
}
