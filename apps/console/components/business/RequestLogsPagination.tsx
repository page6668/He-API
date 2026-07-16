'use client';

/**
 * Story 9.2 AC2 — Prev/Next pagination over the ≤1000-row recent window. Walks
 * limit/offset, honouring has_more (Next disabled when false; Prev disabled at
 * offset 0). The current filters are preserved — only `offset` changes — and the
 * new offset is reflected in the URL (BR-UI-3 shareable/back-safe). Buttons carry
 * aria-labels + disabled states (a11y, BR-UI-1).
 */

import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/kit';

export interface RequestLogsPaginationProps {
  offset: number;
  limit: number;
  hasMore: boolean;
}

export function RequestLogsPagination({ offset, limit, hasMore }: RequestLogsPaginationProps) {
  const t = useTranslations('logs');
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const page = Math.floor(offset / Math.max(limit, 1)) + 1;
  const canPrev = offset > 0;

  function goto(nextOffset: number) {
    const params = new URLSearchParams(searchParams.toString());
    if (nextOffset <= 0) {
      params.delete('offset');
    } else {
      params.set('offset', String(nextOffset));
    }
    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  return (
    <nav aria-label={t('pagination.label')} className="flex items-center justify-end gap-3">
      <Button
        variant="secondary"
        aria-label={t('pagination.prev')}
        disabled={!canPrev}
        onClick={() => goto(Math.max(offset - limit, 0))}
      >
        {t('pagination.prev')}
      </Button>
      {/* Active/current state uses ink, never seal (design-system.md distinctive_rule). */}
      <span className="tabular text-small text-ink-secondary" dir="ltr">
        {t('pagination.page', { page })}
      </span>
      <Button
        variant="secondary"
        aria-label={t('pagination.next')}
        disabled={!hasMore}
        onClick={() => goto(offset + limit)}
      >
        {t('pagination.next')}
      </Button>
    </nav>
  );
}
