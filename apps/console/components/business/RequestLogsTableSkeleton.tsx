/**
 * Story 9.2 AC2 — loading skeleton for the request-logs table, shown by the
 * page-level Suspense boundary while getUsageLogs resolves. role="status" +
 * aria-live="polite" announces the loading state once (BR-UI-1 loading state).
 */
export interface RequestLogsTableSkeletonProps {
  /** Localized aria-label (e.g. "Loading request logs…"). */
  label: string;
  /** Number of placeholder rows (default 8). */
  rows?: number;
}

const COLUMN_COUNT = 8;

export function RequestLogsTableSkeleton({ label, rows = 8 }: RequestLogsTableSkeletonProps) {
  return (
    <div
      role="status"
      aria-busy="true"
      aria-live="polite"
      aria-label={label}
      className="overflow-hidden rounded-lg border border-neutral-200"
    >
      <div className="grid grid-cols-8 gap-2 border-b border-neutral-200 bg-neutral-50 p-3">
        {Array.from({ length: COLUMN_COUNT }).map((_, col) => (
          <div key={col} className="h-4 animate-pulse rounded bg-neutral-200" />
        ))}
      </div>
      {Array.from({ length: rows }).map((_, row) => (
        <div key={row} className="grid grid-cols-8 gap-2 border-b border-neutral-100 p-3 last:border-0">
          {Array.from({ length: COLUMN_COUNT }).map((__, col) => (
            <div key={col} className="h-3 animate-pulse rounded bg-neutral-100" />
          ))}
        </div>
      ))}
    </div>
  );
}
