// lib/oauth.ts — Story 2.3 P8 OAuth types + helpers.
//
// The Server Action `initiateOAuth` (in app/[locale]/(auth)/_actions/initiate-oauth.ts)
// validates inputs against these types before reaching api-gateway.

export type OAuthProvider = 'google' | 'github';

export const oauthProviders = ['google', 'github'] as const satisfies readonly OAuthProvider[];

export interface InitiateOAuthInput {
  provider: OAuthProvider;
  /** Optional post-callback destination. Defaults to `/{locale}/dashboard`. */
  returnTo?: string;
  /** Caller locale; defaults to 'en'. */
  locale?: string;
}

export interface InitiateOAuthOutput {
  /** Provider authorize URL — client navigates via window.location.assign. */
  authorizeUrl: string;
}

export class OAuthValidationError extends Error {
  readonly code: string;
  constructor(code: string, message?: string) {
    super(message ?? code);
    this.name = 'OAuthValidationError';
    this.code = code;
  }
}

/**
 * Validate provider input. Throws OAuthValidationError when the provider is
 * not in the canonical set. Server Action invokes this before any network
 * call so an invalid input never reaches api-gateway.
 */
export function assertProvider(p: unknown): asserts p is OAuthProvider {
  if (typeof p !== 'string' || !oauthProviders.includes(p as OAuthProvider)) {
    throw new OAuthValidationError('invalid_provider', `unsupported provider: ${String(p)}`);
  }
}
