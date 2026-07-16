'use client';

import { useState } from 'react';
import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import {
  resendVerificationForm,
  type ResendVerificationResult,
} from '@/app/[locale]/_actions/auth';
import { Button, Notice, fieldCls, labelCls } from '@/components/ui/kit';

/**
 * ResendModal is the client-side dialog the user opens from either:
 *   - the check-inbox page (right after signup, when the email hasn't arrived)
 *   - the verify-email error page (when the link expired or was already used)
 *
 * It binds `resendVerificationForm` (the (prev, formData) → state wrapper)
 * via React 19's useFormState. The per-form-element zod validation lives
 * inside the Server Action; the modal just renders the i18n key the
 * action returns. m-5 anti-enumeration: the success message is the same
 * regardless of whether the email was sent / unknown / already-verified.
 */
/**
 * 刻意用 secondary(不是朱砂):本组件也会挂在 SigninForm 的错误提示里,而那一屏的
 * 朱砂已经给了「登录」。每屏只允许一处朱砂(design-system.md distinctive_rule 铁律2)。
 */
function SubmitButton({ label, submittingLabel }: { label: string; submittingLabel: string }) {
  const { pending } = useFormStatus();
  return (
    <Button type="submit" variant="secondary" disabled={pending} className="w-full">
      {pending ? submittingLabel : label}
    </Button>
  );
}

export function ResendModal() {
  // 命名空间 = 文件名:messages/<locale>/auth.json → `auth`(文案在 auth.verifyEmail.* /
  // auth.signupCheckInbox.* / auth.errors.*)。无参 useTranslations() 会 MISSING_MESSAGE。
  const t = useTranslations('auth');
  const [open, setOpen] = useState(false);
  const [state, formAction] = useFormState<ResendVerificationResult | null, FormData>(
    resendVerificationForm,
    null,
  );

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="text-small text-ink underline decoration-line-strong underline-offset-2 transition-colors duration-state ease-he hover:decoration-ink"
      >
        {t('signupCheckInbox.resendCta')}
      </button>
    );
  }

  // Success branch — the action returned ok=true. Per m-5 we render a
  // generic "we sent you a new link" message — no signal about whether
  // the email is actually registered.
  if (state && state.ok) {
    return (
      // 竹绿(jade)—— 与朱砂刻意拉开明度/色相;行内细条,不是满宽色底 banner。
      <div
        role="status"
        aria-live="polite"
        className="rounded-lg border border-jade/30 bg-jade/5 px-4 py-2.5 text-small text-jade"
      >
        <p className="font-medium">{t('verifyEmail.resendSentTitle')}</p>
        <p className="mt-0.5">{t('verifyEmail.resendSentDescription')}</p>
      </div>
    );
  }

  const errorKey = state && !state.ok ? state.code : undefined;
  const retryAfterSeconds = state && !state.ok ? state.retryAfterSeconds : undefined;

  return (
    // 面板零阴影 —— 层级靠 1px 暖边框(design-system.md shape_elevation.shadow)。
    <div
      role="dialog"
      aria-labelledby="resend-modal-title"
      className="rounded-lg border border-line bg-surface p-4 text-start"
    >
      <div className="flex items-start justify-between gap-3">
        <h2 id="resend-modal-title" className="text-h3 text-ink">
          {t('verifyEmail.resendModalTitle')}
        </h2>
        {/* 关闭:降为 ghost 图标位。可访问名沿用原有 ✕ 字形 —— 本次是纯视觉重构,
            不新增 i18n key(真正的 close 文案需 10 语言 fan-out,另行处理)。 */}
        <button
          type="button"
          onClick={() => setOpen(false)}
          className="-me-1 -mt-1 rounded-md px-2 py-1 text-small leading-none text-ink-muted transition-colors duration-state ease-he hover:bg-surface-sunken hover:text-ink focus:outline-none focus:ring-2 focus:ring-ink/15"
        >
          ✕
        </button>
      </div>
      <p className="mt-1 text-small text-ink-secondary">{t('verifyEmail.resendModalDescription')}</p>
      <form action={formAction} className="mt-4 space-y-4" noValidate>
        {errorKey ? (
          <Notice tone="error" role="alert">
            {t(errorKey.replace(/^auth\./, ''))}
            {retryAfterSeconds ? <span className="tabular"> ({retryAfterSeconds}s)</span> : null}
          </Notice>
        ) : null}
        <div>
          <label htmlFor="resend-email" className={labelCls}>
            {t('signup.emailLabel')}
          </label>
          <input
            id="resend-email"
            name="email"
            type="email"
            required
            autoComplete="email"
            placeholder={t('signup.emailPlaceholder')}
            className={fieldCls}
          />
        </div>
        <SubmitButton
          label={t('verifyEmail.resendSubmit')}
          submittingLabel={t('verifyEmail.resendSubmitting')}
        />
      </form>
    </div>
  );
}
