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
        <div key={col} className="rounded-lg border border-line bg-surface p-5">
          {/* 骨架屏刻意不用 animate-pulse 闪烁(design-system.md motion.use_where 明令排除) —
              静态暖灰块本身即是克制的动效主张。 */}
          <div className="mb-4 h-4 w-20 rounded bg-surface-sunken" />
          <div className="grid grid-cols-2 gap-4">
            {Array.from({ length: 4 }).map((__, row) => (
              // 与真卡同构:metric-lg 大数在上、小 label 在下(M4 仪表化)。
              <div key={row} className="space-y-1.5">
                <div className="h-7 w-20 rounded bg-surface-sunken" />
                <div className="h-3 w-14 rounded bg-surface-sunken" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
