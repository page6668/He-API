'use client';

// Story 2.7 AC3 — "Reactivate My Account" action on the recovery page. Calls
// the cancel Server Action; on success the account is active again and the
// browser returns to the console. Grace-expired (sweeper ran) surfaces the
// permanent-deletion notice.

import { useState, useTransition } from 'react';
import { Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useRouter } from 'next/navigation';

import { Button } from '@/components/ui/button';
import { cancelAccountDeletion, signOutAfterGraceExpiry } from '@/lib/account/deletion-actions';

export function ReactivateAccountButton({ locale }: { locale: string }) {
  const t = useTranslations('account.delete.recovery');
  const router = useRouter();
  const [isPending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);

  function onClick() {
    setError(null);
    startTransition(async () => {
      const res = await cancelAccountDeletion();
      switch (res.kind) {
        case 'ok':
          router.push(`/${locale}/`);
          router.refresh();
          return;
        case 'unauthorized':
          router.push(`/${locale}/signin`);
          return;
        case 'grace_expired':
          // The sweeper ran between page load and this click — the account is
          // gone. Show the expired notice, then terminate the now-orphaned
          // session (clear cookies + redirect to signin) rather than leaving a
          // stale he_access cookie (AC3 error-table / QA-2.7-001).
          setError(t('expired'));
          await signOutAfterGraceExpiry(locale);
          return;
        default:
          setError(t('expired'));
      }
    });
  }

  return (
    <div>
      <Button variant="default" disabled={isPending} onClick={onClick}>
        {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
        {t('reactivate_cta')}
      </Button>
      {error && (
        <p role="alert" className="mt-3 text-sm text-red-600">
          {error}
        </p>
      )}
    </div>
  );
}
