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
    <form aria-labelledby="profile-form-heading" className="space-y-6" onSubmit={onSubmit}>
      <header className="space-y-1">
        <h1 id="profile-form-heading" className="text-2xl font-semibold">
          {t('profile.title')}
        </h1>
      </header>

      {/* Display name */}
      <div className="space-y-2">
        <label htmlFor="display_name" className="block text-sm font-medium">
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
          className="w-full rounded border px-3 py-2"
          maxLength={100}
        />
        {result?.kind === 'validation' && result.fieldErrors?.display_name && (
          <p role="alert" aria-live="polite" className="text-sm text-red-600">
            {t(result.fieldErrors.display_name as never)}
          </p>
        )}
      </div>

      {/* Email (read-only) */}
      <div className="space-y-2">
        <label htmlFor="email" className="block text-sm font-medium">
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
          className="w-full rounded border bg-neutral-50 px-3 py-2 text-neutral-600"
        />
        <p className="text-xs text-neutral-500">{t('profile.email.read_only_hint')}</p>
      </div>

      {/* Locale */}
      <div className="space-y-2">
        <label htmlFor="locale" className="block text-sm font-medium">
          {t('profile.locale.label')}
        </label>
        <select
          id="locale"
          name="locale"
          value={selectedLocale}
          onChange={(e) => setSelectedLocale(e.target.value as Locale)}
          className="w-full rounded border px-3 py-2"
        >
          {locales.map((loc) => (
            <option key={loc} value={loc}>
              {LOCALE_NATIVE_LABELS[loc]}
            </option>
          ))}
        </select>
      </div>

      {/* Timezone */}
      <div className="space-y-2">
        <label htmlFor="timezone" className="block text-sm font-medium">
          {t('profile.timezone.label')}
        </label>
        <select
          id="timezone"
          name="timezone"
          value={selectedTimezone}
          onChange={(e) => setSelectedTimezone(e.target.value)}
          className="w-full rounded border px-3 py-2"
        >
          {getTimezones().map((tz) => (
            <option key={tz} value={tz}>
              {tz}
            </option>
          ))}
        </select>
      </div>

      {/* Badges */}
      <div className="flex flex-wrap gap-2 text-xs">
        <span className="rounded-full bg-neutral-100 px-3 py-1">
          {defaults.totp_enabled
            ? t('profile.security.totp_enabled')
            : t('profile.security.totp_disabled')}
        </span>
        <span className="rounded-full bg-neutral-100 px-3 py-1">
          {defaults.oauth_provider === 'google'
            ? t('profile.auth_method.oauth_google')
            : defaults.oauth_provider === 'github'
              ? t('profile.auth_method.oauth_github')
              : t('profile.auth_method.email_password')}
        </span>
      </div>

      {/* Concurrent-update banner */}
      {result?.kind === 'concurrent_update' && (
        <div role="alert" className="rounded border border-yellow-300 bg-yellow-50 p-3 text-sm">
          {t('profile.banners.concurrent_update.message')}{' '}
          <button
            type="button"
            onClick={() => location.reload()}
            className="font-medium underline"
          >
            {t('profile.banners.concurrent_update.reload_cta')}
          </button>
        </div>
      )}

      {/* Rate limit toast */}
      {result?.kind === 'rate_limited' && (
        <div role="alert" className="rounded border border-orange-300 bg-orange-50 p-3 text-sm">
          {t('profile.errors.rate_limited')}
        </div>
      )}

      {/* Generic / unauthorized errors */}
      {(result?.kind === 'error' || result?.kind === 'unauthorized') && (
        <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm">
          {t('profile.errors.generic')}
        </div>
      )}

      {/* Success */}
      {result?.kind === 'ok' && (
        <div role="status" aria-live="polite" className="rounded border border-green-300 bg-green-50 p-3 text-sm">
          {t('profile.toast.saved')}
        </div>
      )}

      <button
        type="submit"
        disabled={!isDirty || isPending}
        className="rounded bg-neutral-900 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
      >
        {t('profile.actions.save')}
      </button>
    </form>
  );
}
