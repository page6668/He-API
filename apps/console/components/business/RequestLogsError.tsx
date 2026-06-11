'use client';

/**
 * Story 9.2 AC2 — inline error/retry banner for the log viewer. On a 503
 * (ClickHouse unavailable) the page renders this on the table card while the
 * filter bar stays intact above it (BR-UI Error Handling / ERROR-002). The Retry
 * link points at the SAME URL (current filters preserved). role="alert" so the
 * error is announced (a11y).
 */
export interface RequestLogsErrorProps {
  message: string;
  retryLabel: string;
  retryHref: string;
}

export function RequestLogsError({ message, retryLabel, retryHref }: RequestLogsErrorProps) {
  return (
    <div
      role="alert"
      data-testid="request-logs-error"
      className="space-y-2 rounded-lg border border-red-300 bg-red-50 p-4 text-sm text-red-900"
    >
      <p>{message}</p>
      <a
        href={retryHref}
        className="inline-block rounded bg-red-600 px-3 py-1.5 text-white hover:bg-red-700"
      >
        {retryLabel}
      </a>
    </div>
  );
}
