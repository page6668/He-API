'use client';

/**
 * Story 9.2 AC2 — inline error/retry banner for the log viewer. On a 503
 * (ClickHouse unavailable) the page renders this on the table card while the
 * filter bar stays intact above it (BR-UI Error Handling / ERROR-002). The Retry
 * link points at the SAME URL (current filters preserved). role="alert" so the
 * error is announced (a11y).
 *
 * Visual language mirrors kit's <Notice tone="error"> inline strip (not a
 * full-width red banner — knowledge/taste/design-system.md); rendered by hand
 * rather than via the primitive so role/data-testid stay on the same node.
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
      className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-crimson/30 bg-crimson/5 px-4 py-2.5 text-small text-crimson"
    >
      <p>{message}</p>
      <a
        href={retryHref}
        className="shrink-0 rounded-md border border-crimson/40 bg-transparent px-3 py-1.5 text-small font-medium text-crimson transition-colors duration-state ease-he hover:bg-crimson/5 focus:outline-none focus:ring-2 focus:ring-crimson/25"
      >
        {retryLabel}
      </a>
    </div>
  );
}
