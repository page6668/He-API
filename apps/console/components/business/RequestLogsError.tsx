'use client';

/**
 * Story 9.2 AC2 — inline error/retry for the log viewer. On a 503 (ClickHouse
 * unavailable) the page renders this where the table would be, while the filter
 * bar stays intact above it (BR-UI Error Handling / ERROR-002). The Retry link
 * points at the SAME URL (current filters preserved). role="alert" so the error
 * is announced (a11y).
 *
 * M6-B: aligned with the M4 error-state convention (UsageChart/UsageStatCards) —
 * one quiet line of text-small text-ink-secondary + an indigo linkCls retry text
 * link. No boxed Notice/red banner (design-system.md: 错态用一行安静说明 + 文字链)。
 * role/data-testid stay on the same node; retry stays an <a href> (navigation
 * semantics unchanged), only its visual is linkCls.
 */
import { linkCls } from '@/components/ui/kit';

export interface RequestLogsErrorProps {
  message: string;
  retryLabel: string;
  retryHref: string;
}

export function RequestLogsError({ message, retryLabel, retryHref }: RequestLogsErrorProps) {
  return (
    <p role="alert" data-testid="request-logs-error" className="text-small text-ink-secondary">
      {message}{' '}
      <a href={retryHref} className={linkCls}>
        {retryLabel}
      </a>
    </p>
  );
}
