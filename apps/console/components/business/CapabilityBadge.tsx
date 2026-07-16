/**
 * Story 4.7 T4.3 — `CapabilityBadge`.
 *
 * Renders ✓ / ✗ glyph with a paired visually-hidden text label for screen
 * readers (BR-2.7 WCAG 2.1 AA). Color contrast on the badge text passes
 * the 4.5:1 ratio via design-token `text-jade` / `text-ink-muted` against
 * a light background (knowledge/taste/design-system.md — states.success is
 * jade, never the seal accent).
 *
 * Data-testid attributes anchor the Vitest assertions in
 * `apps/console/__tests__/components/business/CapabilityMatrix.test.tsx`
 * (4.7-UNIT-009/011 assert these token class names; they were migrated from
 * `text-green-600` / `text-slate-400` alongside this refactor, 2026-07-16).
 */
import type { JSX } from "react";

interface CapabilityBadgeProps {
  present: boolean;
  label: string;
}

export function CapabilityBadge({ present, label }: CapabilityBadgeProps): JSX.Element {
  if (present) {
    return (
      <span
        data-testid="capability-badge-yes"
        className="inline-flex items-center text-jade"
      >
        <span aria-hidden="true">✓</span>
        <span className="sr-only">{label}</span>
      </span>
    );
  }
  return (
    <span
      data-testid="capability-badge-no"
      className="inline-flex items-center text-ink-muted"
    >
      <span aria-hidden="true">✗</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}
