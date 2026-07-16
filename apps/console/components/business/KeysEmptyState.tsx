'use client';

/**
 * Story 5.5 AC1 — empty state (T1.4).
 *
 * Rendered when the user has zero keys. The CTA opens the same CreateKeyModal
 * as the toolbar [New key] button (the parent wires `onCreate`). Uses kit's
 * <EmptyState> (guidance not apology, brand.md tone_rules). The toolbar
 * [New key] button already owns the screen's one seal — this CTA renders
 * `outline` (quiet) so IRON LAW 2 (one seal per screen) holds even on the
 * empty screen where both are visible at once.
 */

import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/button';
import { EmptyState } from '@/components/ui/kit';

export interface KeysEmptyStateProps {
  onCreate: () => void;
}

export function KeysEmptyState({ onCreate }: KeysEmptyStateProps) {
  const t = useTranslations('account.keys');
  return (
    <EmptyState
      title={t('empty.title')}
      description={t('empty.body')}
      action={
        <Button variant="outline" onClick={onCreate}>
          {t('empty.cta')}
        </Button>
      }
    />
  );
}
