'use client';

// Story 2.7 AC3 (QA-2.7-001) — auto sign-out on the grace-expired recovery
// state. Rendered (returning no UI of its own) alongside the
// {account.delete.recovery.expired} notice on the recovery page: the account
// the sweeper already deleted is gone server-side, so on mount this clears the
// now-orphaned he_access/he_refresh cookies and routes to signin instead of
// leaving the user holding a stale, still-presentable session until JWT TTL.

import { useEffect, useRef } from 'react';

import { signOutAfterGraceExpiry } from '@/lib/account/deletion-actions';

export function GraceExpiredSignOut({ locale }: { locale: string }) {
  const fired = useRef(false);
  useEffect(() => {
    if (fired.current) return; // StrictMode double-invoke guard — sign out once.
    fired.current = true;
    void signOutAfterGraceExpiry(locale);
  }, [locale]);
  return null;
}
