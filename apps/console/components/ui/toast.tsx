'use client';

/**
 * Story 5.5 — minimal presentational Toast.
 *
 * The console has no toast library; each surface owns its toast state and
 * renders this fixed-position region. `success`/`info` announce politely
 * (role="status"); `error` is assertive (role="alert"). Auto-dismisses after
 * `durationMs` (front-end-spec §5.1 default 4s); pass `durationMs={0}` to keep
 * it sticky (e.g. a rate-limit countdown owned by the caller).
 */

import { useEffect, type ReactNode } from 'react';

import { cn } from '@/lib/utils';

export type ToastVariant = 'success' | 'error' | 'info';

export interface ToastProps {
  variant?: ToastVariant;
  onDismiss: () => void;
  durationMs?: number;
  children: ReactNode;
}

const VARIANT_STYLES: Record<ToastVariant, string> = {
  success: 'border-green-300 bg-green-50 text-green-900',
  error: 'border-red-300 bg-red-50 text-red-900',
  info: 'border-blue-300 bg-blue-50 text-blue-900',
};

export function Toast({ variant = 'info', onDismiss, durationMs = 4000, children }: ToastProps) {
  useEffect(() => {
    if (!durationMs) return;
    const id = setTimeout(onDismiss, durationMs);
    return () => clearTimeout(id);
  }, [durationMs, onDismiss]);

  return (
    <div
      role={variant === 'error' ? 'alert' : 'status'}
      aria-live={variant === 'error' ? 'assertive' : 'polite'}
      className={cn(
        'fixed bottom-4 end-4 z-[60] max-w-sm rounded border px-4 py-3 text-sm shadow-lg',
        VARIANT_STYLES[variant],
      )}
    >
      {children}
    </div>
  );
}
