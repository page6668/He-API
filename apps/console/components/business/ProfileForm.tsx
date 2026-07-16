'use client';

// Story 2.5 — Profile form (client component).
//
// Renders the display_name + email (read-only) + locale + timezone fields,
// plus the 2FA + auth-method badges per AC1. On submit, calls the
// updateMyProfile Server Action and renders the result inline.
//
// Locale-change UX (AC3) is handled by the Server Action — the redirect
// throws the Next.js NEXT_REDIRECT signal, which propagates here and causes
// the URL to switch. We do NOT need any client-side router.push.

import { useState, useTransition, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';

import { locales, type Locale } from '@/i18n/config';
import type { Profile } from '@/app/[locale]/(console)/settings/profile/_actions/get-my-profile';
import { updateMyProfile, type UpdateMyProfileResult } from '@/app/[locale]/(console)/settings/profile/_actions/update-my-profile';
import { profileFormSchema, type ProfileFormValues } from './ProfileForm.schema';
import { Button, Notice, Panel, fieldCls, labelCls } from '@/components/ui/kit';
import { cn } from '@/lib/utils';

// Native-language labels for the locale Select. Source-of-truth lives in
// messages/{locale}/common.json under `localeSwitch.options.*`; this list is
// the keying view-model the Select renders.
const LOCALE_NATIVE_LABELS: Record<Locale, string> = {
  en: 'English',
  'zh-CN': '中文',
  ja: '日本語',
  ko: '한국어',
  es: 'Español',
  fr: 'Français',
  de: 'Deutsch',
  pt: 'Português',
  ru: 'Русский',
  ar: 'العربية',
};

// IANA timezone source. Fall back to a static list if the browser is old
// enough not to support Intl.supportedValuesOf (very old; defensive).
function getTimezones(): string[] {
  // Intl.supportedValuesOf landed in ES2022; modern TS libs have the
  // signature. Cast through unknown so this stays compatible with older
  // libdom shipped configurations.
  const intlAny = Intl as unknown as { supportedValuesOf?: (k: string) => string[] };
  let supported = typeof intlAny.supportedValuesOf === 'function'
    ? intlAny.supportedValuesOf('timeZone')
    : ['America/New_York', 'America/Los_Angeles', 'Europe/London', 'Asia/Shanghai', 'Asia/Tokyo'];
  // CLDR / Node Intl normalise UTC to "Etc/UTC" and omit the plain "UTC"
  // alias. Story 2.2 baseline stores users.timezone='UTC' as the DEFAULT;
  // prepend UTC so the controlled <select> can match it on first render
  // and avoid an unintended option fallback.
  if (!supported.includes('UTC')) {
    supported = ['UTC', ...supported];
  }
  return supported;
}

interface ProfileFormProps {
  defaults: Profile;
  etag: string;
  currentLocale: Locale;
}

export function ProfileForm({ defaults, etag, currentLocale }: ProfileFormProps) {
  const t = useTranslations('account');
  const [displayName, setDisplayName] = useState(defaults.display_name ?? '');
  const [selectedLocale, setSelectedLocale] = useState<Locale>(defaults.locale);
  const [selectedTimezone, setSelectedTimezone] = useState(defaults.timezone);
  const [isPending, startTransition] = useTransition();
  const [result, setResult] = useState<UpdateMyProfileResult | null>(null);

  const isDirty =
    displayName !== (defaults.display_name ?? '') ||
    selectedLocale !== defaults.locale ||
    selectedTimezone !== defaults.timezone;

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!isDirty || isPending) return;

    const values: ProfileFormValues = {};
    if (displayName !== (defaults.display_name ?? '')) values.display_name = displayName;
    if (selectedLocale !== defaults.locale) values.locale = selectedLocale;
    if (selectedTimezone !== defaults.timezone) values.timezone = selectedTimezone;

    // Client-side Zod parity — short-circuit on validation errors so we
    // don't burn a network call.
    const parsed = profileFormSchema.safeParse(values);
    if (!parsed.success) {
      const fieldErrors: Partial<Record<keyof ProfileFormValues, string>> = {};
      for (const issue of parsed.error.issues) {
        const key = issue.path[0] as keyof ProfileFormValues;
        fieldErrors[key] = issue.message;
      }
      setResult({ kind: 'validation', fieldErrors });
      return;
    }

    startTransition(async () => {
      const out = await updateMyProfile({
        values: parsed.data,
        ifMatch: etag,
        currentLocale,
      });
      setResult(out);
    });
  }

  return (
    // 标题现由外层 PageShell 渲染(titleId="profile-form-heading"),此处仅保留
    // aria-labelledby 接线,避免重复标题(design-system.md 页面容器规则)。
    <form aria-labelledby="profile-form-heading" className="space-y-6" onSubmit={onSubmit}>
      <Panel className="space-y-5">
        {/* Display name */}
        <div>
          <label htmlFor="display_name" className={labelCls}>
            {t('profile.display_name.label')}
          </label>
          <input
            id="display_name"
            name="display_name"
            type="text"
            value={displayName}
            placeholder={t('profile.display_name.placeholder')}
            onChange={(e) => setDisplayName(e.target.value)}
            aria-invalid={result?.kind === 'validation' && result.fieldErrors?.display_name ? true : undefined}
            className={fieldCls}
            maxLength={100}
          />
          {result?.kind === 'validation' && result.fieldErrors?.display_name && (
            <p role="alert" aria-live="polite" className="mt-1.5 text-small text-crimson">
              {t(result.fieldErrors.display_name as never)}
            </p>
          )}
        </div>

        {/* Email (read-only) */}
        <div>
          <label htmlFor="email" className={labelCls}>
            {t('profile.email.label')}
          </label>
          <input
            id="email"
            name="email"
            type="email"
            value={defaults.email}
            readOnly
            aria-readonly="true"
            title={t('profile.email.read_only_tooltip')}
            className={cn(fieldCls, 'bg-surface-sunken text-ink-secondary')}
          />
          <p className="mt-1.5 text-small text-ink-muted">{t('profile.email.read_only_hint')}</p>
        </div>

        {/* Locale */}
        <div>
          <label htmlFor="locale" className={labelCls}>
            {t('profile.locale.label')}
          </label>
          <select
            id="locale"
            name="locale"
            value={selectedLocale}
            onChange={(e) => setSelectedLocale(e.target.value as Locale)}
            className={fieldCls}
          >
            {locales.map((loc) => (
              <option key={loc} value={loc}>
                {LOCALE_NATIVE_LABELS[loc]}
              </option>
            ))}
          </select>
        </div>

        {/* Timezone */}
        <div>
          <label htmlFor="timezone" className={labelCls}>
            {t('profile.timezone.label')}
          </label>
          <select
            id="timezone"
            name="timezone"
            value={selectedTimezone}
            onChange={(e) => setSelectedTimezone(e.target.value)}
            className={`${fieldCls} font-mono`}
          >
            {getTimezones().map((tz) => (
              <option key={tz} value={tz}>
                {tz}
              </option>
            ))}
          </select>
        </div>

        {/* Badges */}
        <div className="flex flex-wrap gap-2">
          <span className="rounded-full border border-line bg-surface-sunken px-3 py-1 text-label text-ink-secondary">
            {defaults.totp_enabled
              ? t('profile.security.totp_enabled')
              : t('profile.security.totp_disabled')}
          </span>
          <span className="rounded-full border border-line bg-surface-sunken px-3 py-1 text-label text-ink-secondary">
            {defaults.oauth_provider === 'google'
              ? t('profile.auth_method.oauth_google')
              : defaults.oauth_provider === 'github'
                ? t('profile.auth_method.oauth_github')
                : t('profile.auth_method.email_password')}
          </span>
        </div>
      </Panel>

      {/* Concurrent-update banner */}
      {result?.kind === 'concurrent_update' && (
        <Notice tone="warning" role="alert">
          {t('profile.banners.concurrent_update.message')}{' '}
          <button
            type="button"
            onClick={() => location.reload()}
            className="font-medium underline underline-offset-2"
          >
            {t('profile.banners.concurrent_update.reload_cta')}
          </button>
        </Notice>
      )}

      {/* Rate limit toast */}
      {result?.kind === 'rate_limited' && (
        <Notice tone="warning" role="alert">
          {t('profile.errors.rate_limited')}
        </Notice>
      )}

      {/* Generic / unauthorized errors */}
      {(result?.kind === 'error' || result?.kind === 'unauthorized') && (
        <Notice tone="error" role="alert">
          {t('profile.errors.generic')}
        </Notice>
      )}

      {/* Success — jade text, tone=neutral (design-system.md key_page_direction). */}
      {result?.kind === 'ok' && (
        <div
          role="status"
          aria-live="polite"
          className="rounded-lg border border-line bg-surface px-4 py-2.5 text-small text-jade"
        >
          {t('profile.toast.saved')}
        </div>
      )}

      {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2)。 */}
      <Button type="submit" variant="primary" disabled={!isDirty || isPending}>
        {t('profile.actions.save')}
      </Button>
    </form>
  );
}
