/**
 * Story 5.5 AC1 — KeysTableSkeleton (T1.3).
 *
 * Loading placeholder shown inside the page-level Suspense boundary while the
 * Server Component fetches the keys list. Announces once (aria-live polite via
 * aria-busy on the region).
 */

export interface KeysTableSkeletonProps {
  /** Resolved loading label (from the parent Server Component translations). */
  label: string;
  rows?: number;
}

export function KeysTableSkeleton({ label, rows = 3 }: KeysTableSkeletonProps) {
  return (
    <div role="status" aria-busy="true" aria-live="polite" aria-label={label} className="space-y-3">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-center gap-4">
          <div className="h-4 w-32 animate-pulse rounded bg-neutral-200" />
          <div className="h-4 w-24 animate-pulse rounded bg-neutral-200" />
          <div className="h-4 w-20 animate-pulse rounded bg-neutral-200" />
          <div className="ml-auto h-4 w-16 animate-pulse rounded bg-neutral-200" />
        </div>
      ))}
    </div>
  );
}
