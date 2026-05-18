# Evidence 02 — AC1 Dialog accessibility (BR-1.9) gap

**Severity:** HIGH
**ACs affected:** AC1 BR-1.9, BR-1.8

## File

`apps/console/components/business/ExportDataDialog.tsx` — 214 lines, includes inline UI primitives.

## What BR-1.9 requires

> "the Dialog MUST trap focus on open (shadcn-ui default), restore focus to the Export CTA on close, and label itself via `aria-labelledby` pointing to the dialog title (per WCAG 2.1 AA + front-end-spec §6 Accessibility)."

## What's implemented

```tsx
function Dialog({ titleId, onClose, children }: DialogProps) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30"
      onKeyDown={(e) => {
        if (e.key === 'Escape') onClose();
      }}
    >
      <div className="w-full max-w-md rounded bg-white p-5 shadow-lg">{children}</div>
    </div>
  );
}
```

## What's missing

1. **No focus trap.** Real shadcn-ui Dialog uses `@radix-ui/react-dialog` which traps focus via `FocusScope`. The inline primitive has no `useEffect` for focus management. Tab from inside the dialog leaks to the page background.
2. **No focus restoration to CTA.** When dialog closes, focus is lost (returns to `document.body`). Per WCAG 2.4.7 + 2.4.3, focus MUST return to the element that opened the dialog.
3. **ESC handler is on the dialog `<div>`, not at document level.** The dialog div needs keyboard focus for `onKeyDown` to fire. Without a focus trap or auto-focus, ESC won't work when the user is mid-interaction.
4. **No initial focus.** When dialog opens, focus stays on the CTA (which is now obscured by the modal). Screen reader users won't know the dialog opened. Real shadcn-ui auto-focuses the first focusable child or a specified `initialFocusRef`.

## Dev's own acknowledgement

`docs/dev/logs/2.6-dev-log.md` line 60:
> "shadcn-ui-equivalent primitives inlined (TODO swap to real shadcn `<Dialog>` once Story 2.5's chrome lands)."

The TODO is honest, but the comment-block at the bottom of ExportDataDialog.tsx (lines 138-144) claims "this preserves the AC1 contract — focus trap, ESC handling, CTA disabled state" — which is **false**. Only the CTA disabled state is preserved.

## Recommended remediation

Adopt `@radix-ui/react-dialog` (the shadcn-ui base) immediately rather than waiting for Story 2.5's chrome. The package is already a transitive dep in the console; importing it costs ~2 lines.

```tsx
import * as Dialog from '@radix-ui/react-dialog';

<Dialog.Root open={open} onOpenChange={setOpen}>
  <Dialog.Trigger asChild><Button>{t('export.cta.label')}</Button></Dialog.Trigger>
  <Dialog.Portal>
    <Dialog.Overlay className="..."/>
    <Dialog.Content aria-labelledby="export-data-dialog-title">
      <Dialog.Title id="export-data-dialog-title">{t('export.dialog.title')}</Dialog.Title>
      {/* body + buttons */}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
```

This gives focus trap + restore + ESC + Portal + a11y for free.
