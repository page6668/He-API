// Story 2.7 AC3 — account recovery page (Server Component). Shown to a
// pending_deletion user during the 30-day grace (the AC4 guard routes them
// here). Renders the scheduled-deletion date in the user's timezone + a
// whole-days countdown (ICU plural) + the "Reactivate My Account" action.
//
// Path note: this page lives OUTSIDE the (console) route group on purpose — the
// (console) layout guard redirects pending_deletion users to this URL, and a
// page nested under that same guarded layout would re-trigger the redirect
// (App Router layouts don't see the pathname). Hosting it here makes the AC4
// allow-list (INT-016 "recovery is not redirected") naturally loop-free while
// serving the exact URL BR-3.1 specifies: /{locale}/account/recovery.

import { redirect } from 'next/navigation';
import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { getDeletionState } from '@/lib/account/deletion-actions';
import { ReactivateAccountButton } from '@/components/business/ReactivateAccountButton';
import { GraceExpiredSignOut } from '@/components/business/GraceExpiredSignOut';

interface RecoveryPageProps {
  params: { locale: string };
}

export default async function RecoveryPage({ params: { locale } }: RecoveryPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const t = await getTranslations('account.delete.recovery');
  const state = await getDeletionState();

  if (state.kind === 'unauthorized') {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/account/recovery`)}`);
  }

  // status='active' → already reactivated / never pending → back to console.
  if (state.kind === 'ok' && state.data.status === 'active') {
    redirect(`/${resolvedLocale}/`);
  }

  // Grace already elapsed / sweeper ran (deleted, or pending with no date) →
  // recovery is no longer possible.
  const expired =
    state.kind === 'error' ||
    (state.kind === 'ok' && (state.data.status === 'deleted' || !state.data.pending_deletion_at));

  if (expired || state.kind !== 'ok' || !state.data.pending_deletion_at) {
    return (
      <section className="mx-auto max-w-lg space-y-4 py-12">
        <h1 className="text-2xl font-semibold">{t('title')}</h1>
        <p role="alert" className="text-sm text-neutral-700">
          {t('expired')}
        </p>
        {/* AC3 error-table: grace-expired ⇒ show {expired} + signout (QA-2.7-001). */}
        <GraceExpiredSignOut locale={resolvedLocale} />
      </section>
    );
  }

  const deletionDate = new Date(state.data.pending_deletion_at);
  const daysLeft = Math.max(0, Math.ceil((deletionDate.getTime() - Date.now()) / 86_400_000));
  let formattedDate: string;
  try {
    formattedDate = new Intl.DateTimeFormat(resolvedLocale, {
      timeZone: state.data.timezone || 'UTC',
      dateStyle: 'long',
    }).format(deletionDate);
  } catch {
    formattedDate = deletionDate.toISOString();
  }

  return (
    <section className="mx-auto max-w-lg space-y-6 py-12">
      <h1 className="text-2xl font-semibold">{t('title')}</h1>
      <p className="text-base text-neutral-800">{t('countdown', { days: daysLeft })}</p>
      <p className="text-sm text-neutral-500">{formattedDate}</p>
      <p className="text-sm text-neutral-600">{t('explainer')}</p>
      <ReactivateAccountButton locale={resolvedLocale} />
    </section>
  );
}
