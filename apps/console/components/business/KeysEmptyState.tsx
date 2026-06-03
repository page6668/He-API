'use client';

/**
 * Story 5.5 AC1 — empty state (T1.4).
 *
 * Rendered when the user has zero keys. The CTA opens the same CreateKeyModal
 * as the toolbar [New key] button (the parent wires `onCreate`).
 */

import { Key } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/button';

export interface KeysEmptyStateProps {
  onCreate: () => void;
}

export function KeysEmptyState({ onCreate }: KeysEmptyStateProps) {
  const t = useTranslations('account.keys');
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-neutral-300 p-10 text-center">
      <Key className="h-8 w-8 text-neutral-400" aria-hidden="true" />
      <h2 className="text-lg font-semibold">{t('empty.title')}</h2>
      <p className="max-w-sm text-sm text-neutral-600">{t('empty.body')}</p>
      <Button onClick={onCreate}>{t('empty.cta')}</Button>
    </div>
  );
}
