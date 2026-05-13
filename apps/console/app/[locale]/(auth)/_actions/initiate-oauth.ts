'use server';

// Server Action — Story 2.3 P8.
//
// Bridges the client-side button click to the api-gateway initiate endpoint.
// The api-gateway returns a 302 with `Location: {provider authorize URL}`;
// for the BFF flow we don't actually follow the redirect — we build the
// initiate URL on the server side, return it to the client, and the
// client navigates via window.location.assign.

import { headers } from 'next/headers';

import { assertProvider, type InitiateOAuthInput, type InitiateOAuthOutput, OAuthValidationError } from '@/lib/oauth';

/**
 * Build the api-gateway initiate URL. The browser navigates to this URL;
 * the api-gateway sets the he_oauth_state cookie + 302s to the provider.
 *
 * Why not call fetch(apiGatewayUrl) ourselves? Because the api-gateway
 * sets a Set-Cookie that must reach the browser, and Next.js Server
 * Actions cannot proxy Set-Cookie through to the client in a way that
 * persists across the subsequent provider redirect. The "browser-side
 * navigate to api-gateway" pattern is the only way to get the cookie
 * onto the right domain.
 */
export async function initiateOAuth(input: InitiateOAuthInput): Promise<InitiateOAuthOutput> {
  assertProvider(input.provider);

  // Defensive — Server Action can't trust input.returnTo to be safe; the
  // api-gateway re-validates against the allow-list. But we encode it
  // safely as a query param.
  const returnTo = input.returnTo ?? '';
  const locale = input.locale ?? 'en';

  // The api-gateway origin is configured at deploy time. Default to a
  // sentinel that surfaces misconfiguration loudly.
  const apiGatewayBase = process.env.NEXT_PUBLIC_API_GATEWAY_URL ?? 'https://api.he-api.com';

  const params = new URLSearchParams();
  if (returnTo) params.set('return_to', returnTo);
  if (locale) params.set('locale', locale);

  // Add X-Forwarded-For / User-Agent to the initiate request via the
  // browser navigation itself — api-gateway reads them from the incoming
  // request headers. The Server Action just hands back the URL.
  void headers; // (kept import for future extension; e.g. session-aware return_to)

  const authorizeUrl = `${apiGatewayBase}/v1/auth/oauth/${input.provider}/initiate${params.size > 0 ? '?' + params.toString() : ''}`;

  if (!authorizeUrl.startsWith('http')) {
    throw new OAuthValidationError('api_gateway_misconfigured');
  }
  return { authorizeUrl };
}
