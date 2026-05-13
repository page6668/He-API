'use client';

// OAuthButtonGroup — Story 2.3 P8.
//
// Renders the 2 OAuth provider buttons (Google + GitHub) with a locale-
// aware ordering (Asian locales lead with GitHub per AC4 UI Interaction).
// Includes a horizontal divider with "or" copy between the OAuth group
// and the password form (consumer pages mount this above SigninForm /
// SignupForm).

import { useTranslations } from 'next-intl';

import { orderedOAuthButtons } from '@/lib/oauth-button-config';
import { OAuthButton } from './OAuthButton';

interface OAuthButtonGroupProps {
  locale: string;
  /** Optional return_to forwarded to the initiate Server Action. */
  returnTo?: string;
  /** Disabled forwarded from the parent when the password form is busy. */
  disabled?: boolean;
}

export function OAuthButtonGroup({ locale, returnTo, disabled }: OAuthButtonGroupProps) {
  const t = useTranslations('auth');
  const buttons = orderedOAuthButtons(locale);

  return (
    <div className="space-y-3" data-component="OAuthButtonGroup">
      <div className="flex flex-col gap-2">
        {buttons.map((cfg) => (
          <OAuthButton key={cfg.provider} config={cfg} locale={locale} returnTo={returnTo} disabled={disabled} />
        ))}
      </div>
      <div className="relative flex items-center" role="separator" aria-label={t('oauth.divider')}>
        <div className="flex-grow border-t border-neutral-300" />
        <span className="mx-3 text-xs uppercase text-neutral-500">{t('oauth.divider')}</span>
        <div className="flex-grow border-t border-neutral-300" />
      </div>
    </div>
  );
}
