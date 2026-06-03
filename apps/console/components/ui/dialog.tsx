'use client';

/**
 * Story 5.5 — shared accessible Dialog / Drawer primitives.
 *
 * Hand-rolled (the console has no shadcn Dialog/Sheet primitive yet) and
 * lifted from the proven Story-2.6 ExportDataDialog implementation so the
 * focus-management contract (WCAG 2.1 AA) lives in one place:
 *   - focus the first focusable child on open
 *   - restore focus to a caller-supplied trigger (or the previously-focused
 *     element) on close
 *   - document-level ESC closes
 *   - Tab / Shift+Tab focus trap inside the content
 *
 * `Dialog` renders a centred modal; `Drawer` renders a right-side sheet on
 * `>=md` and a full-screen panel on `<md` (Story 5.5 Q-MOBILE1).
 */

import { useEffect, useRef, type ReactNode, type RefObject } from 'react';

import { cn } from '@/lib/utils';

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

function useDialogA11y(
  contentRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  restoreFocusTo?: RefObject<HTMLElement | null>,
  initialFocusRef?: RefObject<HTMLElement | null>,
) {
  useEffect(() => {
    const previouslyFocused = (document.activeElement as HTMLElement | null) ?? null;
    // Capture the restore target at setup — the trigger element is stable for
    // the dialog's lifetime, so this avoids reading a possibly-changed ref in
    // the cleanup.
    const restoreTarget = restoreFocusTo?.current ?? previouslyFocused;
    const root = contentRef.current;
    if (initialFocusRef?.current) {
      initialFocusRef.current.focus();
    } else if (root) {
      const first = root.querySelector<HTMLElement>(FOCUSABLE_SELECTOR);
      (first ?? root).focus();
    }
    return () => {
      // Defer past the React unmount commit so the browser doesn't drop focus
      // onto document.body.
      requestAnimationFrame(() => restoreTarget?.focus());
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [restoreFocusTo]);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const root = contentRef.current;
      if (!root) return;
      const focusables = Array.from(
        root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
      ).filter((el) => !el.hasAttribute('disabled') && el.tabIndex !== -1);
      if (focusables.length === 0) {
        e.preventDefault();
        return;
      }
      const first = focusables[0]!;
      const last = focusables[focusables.length - 1]!;
      const active = document.activeElement as HTMLElement | null;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onClose]);
}

export interface DialogProps {
  titleId: string;
  onClose: () => void;
  /** 'dialog' (default) or 'alertdialog' for destructive confirmations. */
  role?: 'dialog' | 'alertdialog';
  /** Close when the backdrop is clicked. Default true; set false for alertdialog. */
  closeOnOverlay?: boolean;
  restoreFocusTo?: RefObject<HTMLElement | null>;
  /** Focus this element on open instead of the first focusable (e.g. Cancel). */
  initialFocusRef?: RefObject<HTMLElement | null>;
  className?: string;
  children: ReactNode;
}

export function Dialog({
  titleId,
  onClose,
  role = 'dialog',
  closeOnOverlay = true,
  restoreFocusTo,
  initialFocusRef,
  className,
  children,
}: DialogProps) {
  const contentRef = useRef<HTMLDivElement | null>(null);
  useDialogA11y(contentRef, onClose, restoreFocusTo, initialFocusRef);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      onClick={(e) => {
        if (closeOnOverlay && e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={contentRef}
        role={role}
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={cn(
          'w-full max-w-md rounded bg-white p-5 shadow-lg outline-none',
          className,
        )}
      >
        {children}
      </div>
    </div>
  );
}

export interface DrawerProps {
  titleId: string;
  onClose: () => void;
  closeOnOverlay?: boolean;
  restoreFocusTo?: RefObject<HTMLElement | null>;
  children: ReactNode;
}

/** Right-side sheet on `>=md`; full-screen panel on `<md` (Q-MOBILE1). */
export function Drawer({
  titleId,
  onClose,
  closeOnOverlay = true,
  restoreFocusTo,
  children,
}: DrawerProps) {
  const contentRef = useRef<HTMLDivElement | null>(null);
  useDialogA11y(contentRef, onClose, restoreFocusTo);

  return (
    <div
      className="fixed inset-0 z-50 flex justify-end bg-black/30"
      onClick={(e) => {
        if (closeOnOverlay && e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={contentRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className="h-full w-full overflow-y-auto bg-white p-6 shadow-xl outline-none md:w-[min(420px,100vw)]"
      >
        {children}
      </div>
    </div>
  );
}
