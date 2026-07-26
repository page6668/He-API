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

// 色板对齐设计系统:success/error 走 jade/crimson,info 走靛青(indigo 管「信息」)。
// 底色保持不透明(surface / indigo-wash)—— 浮层不透出下层内容。
const VARIANT_STYLES: Record<ToastVariant, string> = {
  success: 'border-jade/40 bg-surface text-jade',
  error: 'border-crimson/40 bg-surface text-crimson',
  info: 'border-indigo/40 bg-indigo-wash text-indigo',
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
        // 真浮层 —— 唯一允许的阴影 token(shadow-overlay);圆角对齐 8px。
        'fixed bottom-4 end-4 z-[60] max-w-sm rounded-lg border px-4 py-3 text-small shadow-overlay',
        VARIANT_STYLES[variant],
      )}
    >
      {children}
    </div>
  );
}
