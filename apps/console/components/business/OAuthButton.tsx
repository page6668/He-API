'use client';

// OAuthButton — Story 2.3 P8 single-provider button component.
//
// Used by <OAuthButtonGroup>. Renders a brand-icon + i18n label + loading
// spinner. Calls the `initiateOAuth` Server Action when clicked; on
// success, navigates the browser via window.location.assign so the
// api-gateway can set he_oauth_state on the right origin.

import { useState, useTransition } from 'react';
import { useTranslations } from 'next-intl';

import { initiateOAuth } from '@/app/[locale]/(auth)/_actions/initiate-oauth';
import type { OAuthButtonConfig } from '@/lib/oauth-button-config';

interface OAuthButtonProps {
  config: OAuthButtonConfig;
  /** Optional post-callback destination passed to the api-gateway. */
  returnTo?: string;
  locale: string;
  /** Disabled state forwarded from the parent (e.g. when password form is submitting). */
  disabled?: boolean;
}

export function OAuthButton({ config, returnTo, locale, disabled }: OAuthButtonProps) {
  const t = useTranslations('auth');
  const [pending, startTransition] = useTransition();
  const [inFlight, setInFlight] = useState(false);
  const isBusy = pending || inFlight;

  function handleClick() {
    if (isBusy) return; // Double-click guard (BLIND-FLOW-002)
    setInFlight(true);
    startTransition(async () => {
      try {
        const { authorizeUrl } = await initiateOAuth({ provider: config.provider, returnTo, locale });
        window.location.assign(authorizeUrl);
      } catch {
        setInFlight(false);
      }
    });
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      disabled={disabled || isBusy}
      aria-label={t(config.ariaLabelKey ?? config.labelKey)}
      data-provider={config.provider}
      // 次级(描边白底 + 品牌图标)—— OAuth 不与朱砂主操作抢
      // (design-system.md key_page_direction.auth)。样式对齐 kit 的 secondary variant。
      className={[
        'inline-flex w-full items-center justify-center gap-2 rounded-md border border-line-strong px-4 py-2.5 text-small font-medium',
        'transition-colors duration-state ease-he',
        config.bgClass,
        config.hoverClass,
        config.textClass,
        'disabled:opacity-50 disabled:cursor-not-allowed',
        // 焦点环与 kit Button 统一(M1:focus-visible 朱砂 ring,永不移除)。
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 focus-visible:ring-offset-2',
      ].join(' ')}
    >
      <span
        className="inline-flex items-center justify-center"
        // brand SVG — provider asset already aria-hidden
        // eslint-disable-next-line react/no-danger
        dangerouslySetInnerHTML={{ __html: config.iconSvg }}
      />
      <span>{isBusy ? '…' : t(config.labelKey)}</span>
    </button>
  );
}
