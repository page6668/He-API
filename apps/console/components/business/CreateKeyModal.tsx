'use client';

/**
 * Story 5.5 AC2 — CreateKeyModal (T2.1).
 *
 * Single `name` input → createMyKey() Server Action → on 201 navigates to the
 * one-time-display sub-page carrying the plaintext in the URL searchParams
 * (Q-U1). Client-side validation mirrors Story 5.1 BR-1.7 via KeyNameSchema
 * (server is the source of truth). 429 surfaces a live countdown and disables
 * Save until it elapses (Q-RL1, integer seconds).
 */

import { useEffect, useState, useTransition, type FormEvent, type RefObject } from 'react';
import { useRouter } from 'next/navigation';
import { Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button, Notice, fieldCls, labelCls } from '@/components/ui/kit';
import { Dialog } from '@/components/ui/dialog';
import { KeyNameSchema } from '@/lib/api/me-keys';
import { createMyKey } from '@/app/[locale]/(console)/keys/_actions/create-key';

export interface CreateKeyModalProps {
  locale: string;
  onClose: () => void;
  triggerRef?: RefObject<HTMLElement | null>;
}

export function CreateKeyModal({ locale, onClose, triggerRef }: CreateKeyModalProps) {
  const t = useTranslations('account.keys');
  const tRoot = useTranslations();
  const router = useRouter();
  const [name, setName] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [retryAfter, setRetryAfter] = useState(0);
  const [isPending, startTransition] = useTransition();

  // Rate-limit countdown tick.
  useEffect(() => {
    if (retryAfter <= 0) return;
    const id = setInterval(() => setRetryAfter((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(id);
  }, [retryAfter]);

  const clientValid = KeyNameSchema.safeParse(name).success;
  const disabled = !clientValid || isPending || retryAfter > 0;

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (disabled) return;
    setFieldError(null);
    setToast(null);

    const parsed = KeyNameSchema.safeParse(name);
    if (!parsed.success) {
      setFieldError(parsed.error.issues[0]?.message ?? 'account.keys.errors.name.invalid_chars');
      return;
    }

    startTransition(async () => {
      const result = await createMyKey({ name: parsed.data });
      if (result.ok) {
        const qs = new URLSearchParams({ plaintext: result.response.plaintext });
        router.push(`/${locale}/keys/${result.response.api_key_id}/created?${qs.toString()}`);
        return;
      }
      const { code, retryAfterSeconds } = result.error;
      if (code === 'account.keys.errors.rate_limited') {
        setRetryAfter(retryAfterSeconds ?? 0);
        setToast(t('create.rate_limited', { seconds: retryAfterSeconds ?? 0 }));
      } else if (code.startsWith('account.keys.errors.name.')) {
        setFieldError(code);
      } else {
        setToast(tRoot(code));
      }
    });
  }

  return (
    <Dialog
      titleId="create-key-modal-title"
      onClose={onClose}
      restoreFocusTo={triggerRef}
      className="rounded-lg border border-line bg-surface shadow-overlay"
    >
      <form onSubmit={onSubmit} className="space-y-4">
        <h2 id="create-key-modal-title" className="text-h3 text-ink">
          {t('create.title')}
        </h2>

        <div className="space-y-1.5">
          <label htmlFor="key-name-input" className={labelCls}>
            {t('create.name_label')}
          </label>
          <input
            id="key-name-input"
            name="name"
            type="text"
            autoFocus
            value={name}
            maxLength={100}
            placeholder={t('create.name_placeholder')}
            onChange={(e) => {
              setName(e.target.value);
              if (fieldError) setFieldError(null);
            }}
            aria-required="true"
            aria-invalid={fieldError ? true : undefined}
            aria-describedby={fieldError ? 'key-name-error' : undefined}
            className={fieldCls}
          />
          {fieldError && (
            <p id="key-name-error" role="alert" className="text-label text-crimson">
              {tRoot(fieldError)}
            </p>
          )}
        </div>

        {toast && (
          <Notice tone="warning" role="alert">
            {toast}
          </Notice>
        )}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose} disabled={isPending}>
            {t('create.cancel')}
          </Button>
          {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2) */}
          <Button type="submit" variant="primary" disabled={disabled}>
            {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
            {t('create.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
