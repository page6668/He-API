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
import { RoutingStrategyForm } from '@/components/business/RoutingStrategyForm';
import { PageShell, Notice } from '@/components/ui/kit';

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

  // Hoisted so both the error branch and the success branch can title the
  // PageShell (the previous version fetched this twice, only in the error
  // branch — pure visual-refactor plumbing, no translation content changed).
  const t = await getTranslations('account');

  if (result.kind === 'error') {
    return (
      <main aria-labelledby="profile-form-heading">
        <PageShell title={t('profile.title')} titleId="profile-form-heading" width="console">
          <Notice tone="error" role="alert">
            {t('profile.errors.load_failed')}
          </Notice>
        </PageShell>
      </main>
    );
  }

  return (
    <main aria-labelledby="profile-form-heading">
      <PageShell title={t('profile.title')} titleId="profile-form-heading" width="console">
        <div className="space-y-10">
          <ProfileForm
            defaults={result.data}
            etag={result.etag}
            currentLocale={resolvedLocale}
          />
          {/* Story 6.5 — account-level default routing strategy (Q-H: inline card on
              the existing settings surface). Shares the profile etag for the
              optimistic-concurrency If-Match contract. */}
          <section className="border-t border-line pt-10">
            <RoutingStrategyForm
              defaultRoutingStrategy={result.data.default_routing_strategy ?? null}
              etag={result.etag}
              currentLocale={resolvedLocale}
            />
          </section>
        </div>
      </PageShell>
    </main>
  );
}
