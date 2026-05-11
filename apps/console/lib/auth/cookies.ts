// Auth cookie attribute builder (Story 2.2 BR-3.7, TS-CONS-007).
//
// api-gateway is the canonical Set-Cookie issuer (Wright Round 1 Q1 ruling).
// This module exists so the console Server Actions / Route Handlers can apply
// the SAME attribute matrix when they need to interrogate or invalidate the
// cookies locally (e.g. signout in P5/T4 — currently a stub).
//
// Two cookies are issued by api-gateway after a successful signin:
//
//   he_access  — JWT RS256 access token. SameSite=Lax, Path=/, Max-Age=900s.
//   he_refresh — JWT RS256 refresh token. SameSite=Strict, Path=/v1/auth/refresh,
//                Max-Age=2592000s.
//
// HttpOnly + Secure (in non-dev) on both, per BR-3.7. Domain mirrors the
// LocaleSwitch cookie pattern from Story 2.1: .he-api.com / .staging.he-api.com
// / undefined for dev.

import type { Env } from '@/lib/i18n';

export const ACCESS_COOKIE = 'he_access';
export const REFRESH_COOKIE = 'he_refresh';

export const ACCESS_MAX_AGE = 900;        // 15 min
export const REFRESH_MAX_AGE = 2_592_000; // 30 days

export interface AccessCookieOptions {
  name: typeof ACCESS_COOKIE;
  path: '/';
  maxAge: number;
  sameSite: 'lax';
  httpOnly: true;
  secure: boolean;
  domain: string | undefined;
}

export interface RefreshCookieOptions {
  name: typeof REFRESH_COOKIE;
  path: '/v1/auth/refresh';
  maxAge: number;
  sameSite: 'strict';
  httpOnly: true;
  secure: boolean;
  domain: string | undefined;
}

function cookieDomain(env: Env): string | undefined {
  if (env === 'production') return '.he-api.com';
  if (env === 'staging') return '.staging.he-api.com';
  return undefined;
}

export function buildAccessCookieOptions(env: Env): AccessCookieOptions {
  return {
    name: ACCESS_COOKIE,
    path: '/',
    maxAge: ACCESS_MAX_AGE,
    sameSite: 'lax',
    httpOnly: true,
    secure: env !== 'development',
    domain: cookieDomain(env),
  };
}

export function buildRefreshCookieOptions(env: Env): RefreshCookieOptions {
  return {
    name: REFRESH_COOKIE,
    path: '/v1/auth/refresh',
    maxAge: REFRESH_MAX_AGE,
    sameSite: 'strict',
    httpOnly: true,
    secure: env !== 'development',
    domain: cookieDomain(env),
  };
}
