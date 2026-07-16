// lib/oauth-button-config.ts — Story 2.3 P8.
//
// Provider button configs (brand colors, SVG icons, i18n keys, locale-
// aware ordering). Asian locales (zh-CN / ja / ko) lead with GitHub per
// AC4 UI Interaction row 2 — GitHub usage among devs in Asian markets
// is higher than Google OAuth share.

import type { OAuthProvider } from './oauth';

export interface OAuthButtonConfig {
  provider: OAuthProvider;
  /** i18n key for the button label, relative to the `auth` namespace. */
  labelKey: `oauth.${'continueWithGoogle' | 'continueWithGitHub'}`;
  /** Inline SVG. Brand-compliant per Google + GitHub guidelines. */
  iconSvg: string;
  /**
   * Tailwind classes for background / text / hover — SECONDARY treatment
   * (描边白底 + 品牌图标). The provider's brand color lives in the icon only;
   * the button surface stays paper-white so it never competes with the
   * seal-red primary (design-system.md key_page_direction.auth).
   */
  bgClass: string;
  hoverClass: string;
  textClass: string;
  /** ARIA label key (falls back to labelKey when omitted). */
  ariaLabelKey?: `oauth.${'continueWithGoogle' | 'continueWithGitHub'}`;
}

// Google "G" logo — the 4-color mark on a white/outlined button. This is the
// other officially sanctioned variant of the Google Sign-In Button Branding
// Guidelines, and the one the design system requires: OAuth is a SECONDARY
// action here, so it may not carry a solid brand-color tile that competes
// with the seal-red primary (design-system.md distinctive_rule 铁律2).
const GOOGLE_ICON = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 18 18" width="18" height="18" aria-hidden="true"><path fill="#4285F4" d="M17.64 9.2c0-.637-.057-1.251-.164-1.84H9v3.481h4.844a4.14 4.14 0 0 1-1.796 2.716v2.258h2.908c1.702-1.567 2.684-3.875 2.684-6.615Z"/><path fill="#34A853" d="M9 18c2.43 0 4.467-.806 5.956-2.18l-2.908-2.259c-.806.54-1.837.86-3.048.86-2.344 0-4.328-1.584-5.036-3.711H.957v2.332A8.997 8.997 0 0 0 9 18Z"/><path fill="#FBBC05" d="M3.964 10.71A5.41 5.41 0 0 1 3.682 9c0-.593.102-1.17.282-1.71V4.958H.957A8.996 8.996 0 0 0 0 9c0 1.452.348 2.827.957 4.042l3.007-2.332Z"/><path fill="#EA4335" d="M9 3.58c1.321 0 2.508.454 3.44 1.345l2.582-2.58C13.463.891 11.426 0 9 0A8.997 8.997 0 0 0 .957 4.958L3.964 7.29C4.672 5.163 6.656 3.58 9 3.58Z"/></svg>`;

// GitHub Octocat — monochrome (black on white per GitHub usage).
const GITHUB_ICON = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M12 0a12 12 0 0 0-3.79 23.39c.6.11.82-.26.82-.58v-2.03c-3.34.73-4.04-1.61-4.04-1.61-.55-1.39-1.34-1.76-1.34-1.76-1.09-.74.08-.73.08-.73 1.2.09 1.84 1.24 1.84 1.24 1.07 1.84 2.81 1.31 3.5 1 .11-.78.42-1.31.76-1.61-2.66-.3-5.47-1.33-5.47-5.94 0-1.31.47-2.38 1.23-3.22-.12-.3-.54-1.52.12-3.17 0 0 1.01-.32 3.3 1.23a11.46 11.46 0 0 1 6 0c2.29-1.55 3.3-1.23 3.3-1.23.66 1.65.24 2.87.12 3.17.77.84 1.23 1.91 1.23 3.22 0 4.62-2.82 5.63-5.5 5.92.43.37.81 1.1.81 2.22v3.29c0 .32.21.7.83.58A12 12 0 0 0 12 0Z"/></svg>`;

export const GOOGLE_CONFIG: OAuthButtonConfig = {
  provider: 'google',
  labelKey: 'oauth.continueWithGoogle',
  iconSvg: GOOGLE_ICON,
  bgClass: 'bg-surface',
  hoverClass: 'hover:border-ink-muted',
  textClass: 'text-ink',
};

export const GITHUB_CONFIG: OAuthButtonConfig = {
  provider: 'github',
  labelKey: 'oauth.continueWithGitHub',
  iconSvg: GITHUB_ICON,
  bgClass: 'bg-surface',
  hoverClass: 'hover:border-ink-muted',
  textClass: 'text-ink',
};

/**
 * Locale-aware button ordering per AC4 UI Interaction row 2.
 * zh-CN / ja / ko developers skew toward GitHub OAuth → render GitHub first.
 * Other locales lead with Google.
 */
export function orderedOAuthButtons(locale: string): OAuthButtonConfig[] {
  const asianLocales: ReadonlySet<string> = new Set(['zh-CN', 'ja', 'ko']);
  return asianLocales.has(locale)
    ? [GITHUB_CONFIG, GOOGLE_CONFIG]
    : [GOOGLE_CONFIG, GITHUB_CONFIG];
}
