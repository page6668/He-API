// Story 2.6 AC1 — Settings → Data page (Server Component).
//
// Renders the data-export landing page with the "Export My Data" CTA +
// confirmation dialog. The current-export state is hydrated on the
// Server Component via getCurrentExport (BR-1.5) so the CTA's disabled
// state is correct on first paint — no client-side loading flash.

import { redirect } from 'next/navigation';
import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { getCurrentExport } from './_actions/get-current-export';
import { ExportDataDialog } from '@/components/business/ExportDataDialog';
import { DeleteAccountDialog } from '@/components/business/DeleteAccountDialog';
import { getDeletionState } from '@/lib/account/deletion-actions';
import { getMyProfile } from '../profile/_actions/get-my-profile';

interface DataPageProps {
  params: { locale: string };
}

export default async function DataPage({ params: { locale } }: DataPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const result = await getCurrentExport();

  if (result.kind === 'unauthorized') {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/data`)}`);
  }

  const t = await getTranslations('account.data');

  if (result.kind === 'error') {
    return (
      <section className="space-y-4 py-8">
        <h1 className="text-2xl font-semibold">{t('title')}</h1>
        <p role="alert" className="text-sm text-red-600">
          {t('export.errors.generic')}
        </p>
      </section>
    );
  }

  // Story 2.7 AC1 — prefetch the account shape + email for the Danger Zone
  // (server-authoritative re-auth field branching; the client never guesses).
  // Failures degrade gracefully: the Danger Zone is simply omitted.
  const [deletion, profile] = await Promise.all([getDeletionState(), getMyProfile()]);

  return (
    <section className="space-y-6 py-8">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold">{t('title')}</h1>
        <p className="text-sm text-neutral-700">{t('description')}</p>
      </header>
      <ExportDataDialog currentExport={result.data} />
      {deletion.kind === 'ok' && profile.kind === 'ok' && (
        <DeleteAccountDialog state={deletion.data} email={profile.data.email} locale={resolvedLocale} />
      )}
    </section>
  );
}
