/**
 * Story 9.1 AC3 — loading skeleton for the dashboard stat-card band, shown by the
 * page-level Suspense boundary while getUsageSummary resolves. role="status" +
 * aria-live="polite" announces the loading state once (BR-UI loading state).
 */
export interface UsageStatCardsSkeletonProps {
  /** Localized aria-label (e.g. "Loading usage…"). */
  label: string;
}

export function UsageStatCardsSkeleton({ label }: UsageStatCardsSkeletonProps) {
  return (
    <div
      role="status"
      aria-busy="true"
      aria-live="polite"
      aria-label={label}
      className="grid grid-cols-1 gap-4 md:grid-cols-3"
    >
      {Array.from({ length: 3 }).map((_, col) => (
        <div key={col} className="rounded-lg border border-neutral-200 p-4">
          <div className="mb-3 h-4 w-20 animate-pulse rounded bg-neutral-200" />
          <div className="space-y-2">
            {Array.from({ length: 4 }).map((__, row) => (
              <div key={row} className="flex items-center justify-between gap-2">
                <div className="h-3 w-16 animate-pulse rounded bg-neutral-200" />
                <div className="h-5 w-12 animate-pulse rounded bg-neutral-200" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
