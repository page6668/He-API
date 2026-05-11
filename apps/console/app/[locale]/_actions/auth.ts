'use server';

// Auth Server Actions (Story 2.2). Per the Wright Round 1 Q1 ruling, the
// console BFF never holds a gRPC client — it goes through api-gateway over
// REST. These actions:
//
//   1. zod-validate the typed input (BR-1.8 + UNIT-056)
//   2. POST to {gateway}/v1/auth/<route> as JSON (UNIT-057)
//   3. translate the JSON envelope → a typed `ActionResult` the form
//      surface renders
//   4. on success, redirect via Next.js `redirect()` (UNIT-058)
//
// P2g lands `registerUser` (+ its form-binding wrapper). Other actions
// remain `NotYetImplemented` until P3 / P4.

import { redirect } from 'next/navigation';

import { defaultLocale, isLocale, type Locale } from '@/i18n/config';
import {
  signupSchema,
  signinSchema,
  resendVerificationSchema,
  verifyEmailTokenSchema,
  type SignupInput,
  type SigninInput,
  type ResendVerificationInput,
  type VerifyEmailTokenInput,
} from '@/lib/auth/schemas';

class NotYetImplemented extends Error {
  constructor(action: string, phase: string) {
    super(`${action}: pending ${phase}`);
    this.name = 'NotYetImplemented';
  }
}

// --- registerUser (P2g, AC1) --------------------------------------------

/**
 * ActionResult is what `registerUser` returns to the caller for the FAILURE
 * paths. Success terminates via Next.js `redirect()` (the function returns
 * `never` in that case).
 *
 * `code` is an i18n key (`auth.errors.*`) so the form can render the
 * localized message without the action knowing about the current locale's
 * messages bundle. `fieldErrors` carries per-field zod issues so the form
 * can highlight `email` vs `password` independently.
 */
export type RegisterUserResult =
  | { ok: false; code: string; fieldErrors?: Record<'email' | 'password' | 'locale', string | undefined>; retryAfterSeconds?: number }
  | { ok: true; redirectTo: string };

// envOr — small helper because Next.js `process.env` is statically inlined
// at build time; at runtime we read the resolved env name.
function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

/**
 * registerUser is the typed entry point used by tests + the form wrapper.
 * On success it redirects to `/<locale>/(auth)/signup/check-inbox?email=<masked>`;
 * the redirect never returns. On validation / network / business failure it
 * returns a typed `RegisterUserResult` whose `code` field is the i18n key
 * the form surfaces.
 */
export async function registerUser(input: SignupInput): Promise<RegisterUserResult> {
  // 1. zod validation BEFORE any network call (UNIT-056, BR-1.8).
  const parsed = signupSchema.safeParse(input);
  if (!parsed.success) {
    const fieldErrors: Record<'email' | 'password' | 'locale', string | undefined> = {
      email: undefined,
      password: undefined,
      locale: undefined,
    };
    let primaryCode = 'auth.errors.invalidEmail';
    for (const issue of parsed.error.issues) {
      const path = issue.path[0];
      if (path === 'email') {
        fieldErrors.email = 'auth.errors.invalidEmail';
        primaryCode = 'auth.errors.invalidEmail';
      } else if (path === 'password') {
        fieldErrors.password = 'auth.errors.passwordTooShort';
        if (primaryCode === 'auth.errors.invalidEmail' && !fieldErrors.email) {
          primaryCode = 'auth.errors.passwordTooShort';
        }
      } else if (path === 'locale') {
        fieldErrors.locale = 'auth.errors.invalidLocale';
      }
    }
    return { ok: false, code: primaryCode, fieldErrors };
  }
  const data = parsed.data;
  const locale: Locale = (data.locale ?? defaultLocale) as Locale;

  // 2. POST to api-gateway (UNIT-057).
  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/auth/signup`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        email: data.email,
        password: data.password,
        locale,
      }),
      // Server Action runs server-side; no `credentials: 'include'` needed.
      cache: 'no-store',
    });
  } catch (_err) {
    return { ok: false, code: 'auth.errors.serverUnavailable' };
  }

  // 3. Translate the envelope to a typed result.
  if (res.status === 200) {
    // Next.js route groups `(auth)` are EXCLUDED from URLs by convention, so
    // even though the page lives at app/[locale]/(auth)/signup/check-inbox/,
    // the URL is /<locale>/signup/check-inbox. The QA Story scenario writes
    // `(auth)` literally — that's a spec-language convention denoting the
    // physical directory, not a URL segment. See Dev Log §12 for the note.
    const target = `/${locale}/signup/check-inbox?email=${encodeURIComponent(maskEmail(data.email))}`;
    redirect(target); // throws — function returns `never` after this point
  }

  let body: unknown;
  try {
    body = await res.json();
  } catch {
    return { ok: false, code: 'auth.errors.serverUnavailable' };
  }
  const code = extractEnvelopeCode(body);
  const retryAfterSeconds = parseRetryAfter(res.headers.get('Retry-After'));
  const i18nCode = mapStatusCodeToI18n(code, res.status);

  if (res.status === 429) {
    return { ok: false, code: i18nCode, retryAfterSeconds };
  }
  return { ok: false, code: i18nCode };
}

/**
 * registerUserForm is the React 19 / Next.js form-action signature. It
 * extracts the form fields, dispatches to `registerUser`, and returns the
 * `RegisterUserResult` for the form surface.
 *
 * Locale is supplied by the caller (the page wraps this action and binds
 * the URL locale into the form data via a hidden input).
 */
export async function registerUserForm(
  _prev: RegisterUserResult | null,
  formData: FormData,
): Promise<RegisterUserResult> {
  const rawLocale = String(formData.get('locale') ?? defaultLocale);
  const locale = isLocale(rawLocale) ? rawLocale : defaultLocale;
  return registerUser({
    email: String(formData.get('email') ?? ''),
    password: String(formData.get('password') ?? ''),
    locale,
  });
}

// --- helpers (kept in this module so the surface for `'use server'`
// re-exports stays minimal) ----------------------------------------------

/**
 * maskEmail returns "j****@example.com" for "john@example.com". The
 * check-inbox page uses this so the URL doesn't leak the full address into
 * browser history. Local-part length ≤ 1 → keep one character, mask the
 * rest with asterisks of the same length as the original (within reason).
 */
export function maskEmail(email: string): string {
  const at = email.indexOf('@');
  if (at <= 0) return email;
  const local = email.slice(0, at);
  const domain = email.slice(at);
  if (local.length <= 1) return `${local}*${domain}`;
  const visible = local[0];
  const maskedCount = Math.min(local.length - 1, 6);
  return `${visible ?? ''}${'*'.repeat(maskedCount)}${domain}`;
}

// extractEnvelopeCode pulls out `error.code` from the OpenAI-compatible
// envelope the gateway emits; defensive on malformed bodies.
function extractEnvelopeCode(body: unknown): string {
  if (typeof body !== 'object' || body === null) return '';
  const errField = (body as { error?: unknown }).error;
  if (typeof errField !== 'object' || errField === null) return '';
  const code = (errField as { code?: unknown }).code;
  return typeof code === 'string' ? code : '';
}

function parseRetryAfter(headerValue: string | null): number | undefined {
  if (!headerValue) return undefined;
  const n = Number.parseInt(headerValue, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

/**
 * mapStatusCodeToI18n converts the gateway's canonical NNN_xxx code into
 * an `auth.errors.*` i18n key (AC1 Error Handling table). The HTTP status
 * is a defensive fallback for the few cases where the gateway emits a
 * raw status with no code body (e.g., proxy timeouts).
 */
function mapStatusCodeToI18n(code: string, httpStatus: number): string {
  switch (code) {
    case '400_invalid_email':
      return 'auth.errors.invalidEmail';
    case '400_invalid_locale':
      return 'auth.errors.invalidLocale';
    case '400_password_too_short':
      return 'auth.errors.passwordTooShort';
    case '400_password_breached':
      return 'auth.errors.passwordBreached';
    case '429_rate_limit_signup':
    case '429_rate_limit_signin_ip':
    case '429_rate_limit_signin_email':
    case '429_rate_limit_resend_ip':
    case '429_rate_limit_resend_email':
      return 'auth.errors.tooManyAttempts';
    case '500_email_send_failed':
      return 'auth.errors.emailSendFailed';
    case '502_auth_svc_unavailable':
      return 'auth.errors.serverUnavailable';
    case '503_hibp_unavailable':
      return 'auth.errors.hibpUnavailable';
  }
  // Fallback per HTTP status.
  if (httpStatus >= 500) return 'auth.errors.serverUnavailable';
  if (httpStatus === 429) return 'auth.errors.tooManyAttempts';
  if (httpStatus === 400) return 'auth.errors.invalidEmail';
  return 'auth.errors.serverUnavailable';
}

// --- verifyEmailAction (P3d, AC2) ----------------------------------------

/**
 * VerifyEmailResult is the typed outcome of `verifyEmailAction`. Unlike
 * `registerUser`, this one does NOT redirect on success — the verify-email
 * page is a Server Component that awaits the action on render and renders
 * the success / already-verified / error UI directly. (UNIT-102 + AC2 UI
 * Interaction — verification must work without JS.)
 */
export type VerifyEmailResult =
  | { ok: true; status: 'email_verified' | 'already_verified'; userId: string; emailVerifiedAt: string }
  | { ok: false; code: string };

/**
 * verifyEmailAction GETs the gateway's `/v1/auth/verify-email?token=…`
 * endpoint and translates the OpenAI-compatible envelope into a typed
 * result. The Server Component that wraps this action chooses success vs
 * already-verified vs resend-CTA UI based on the returned shape.
 */
export async function verifyEmailAction(input: VerifyEmailTokenInput): Promise<VerifyEmailResult> {
  const parsed = verifyEmailTokenSchema.safeParse(input);
  if (!parsed.success) {
    return { ok: false, code: 'auth.errors.invalidToken' };
  }

  let res: Response;
  try {
    res = await fetch(
      `${gatewayURL()}/v1/auth/verify-email?token=${encodeURIComponent(parsed.data.token)}`,
      { method: 'GET', cache: 'no-store' },
    );
  } catch {
    return { ok: false, code: 'auth.errors.serverUnavailable' };
  }

  if (res.status === 200) {
    let body: unknown;
    try {
      body = await res.json();
    } catch {
      return { ok: false, code: 'auth.errors.serverUnavailable' };
    }
    const userID = pickString(body, 'user_id');
    const emailVerifiedAt = pickString(body, 'email_verified_at');
    const status = pickString(body, 'status');
    if (status !== 'email_verified' && status !== 'already_verified') {
      return { ok: false, code: 'auth.errors.serverUnavailable' };
    }
    return { ok: true, status, userId: userID, emailVerifiedAt };
  }

  let body: unknown;
  try {
    body = await res.json();
  } catch {
    /* fall through with empty code */
  }
  const code = extractEnvelopeCode(body);
  return { ok: false, code: mapVerifyCodeToI18n(code, res.status) };
}

// --- resendVerification (P3d, AC2) ---------------------------------------

/**
 * ResendVerificationResult is the typed outcome of `resendVerification`.
 * On the 2xx path the gateway always returns `{status:"ok"}` (m-5 anti-
 * enumeration); the only distinguishing failure is 429 with Retry-After.
 */
export type ResendVerificationResult =
  | { ok: true }
  | { ok: false; code: string; retryAfterSeconds?: number };

/**
 * resendVerification POSTs the gateway's `/v1/auth/resend-verification`
 * endpoint with the supplied email. The action returns `{ok:true}` for
 * every 2xx; the UI cannot distinguish between sent / unknown_email /
 * already_verified (the Wright Round 1 m-5 ruling). 429 surfaces as
 * `tooManyResendAttempts` (IP) or `resendTooSoon` (per-email) with the
 * Retry-After countdown for the form to render.
 */
export async function resendVerification(input: ResendVerificationInput): Promise<ResendVerificationResult> {
  const parsed = resendVerificationSchema.safeParse(input);
  if (!parsed.success) {
    return { ok: false, code: 'auth.errors.invalidEmail' };
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/auth/resend-verification`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: parsed.data.email }),
      cache: 'no-store',
    });
  } catch {
    return { ok: false, code: 'auth.errors.serverUnavailable' };
  }

  if (res.status === 200) {
    return { ok: true };
  }

  let body: unknown;
  try {
    body = await res.json();
  } catch {
    /* fall through */
  }
  const code = extractEnvelopeCode(body);
  const retryAfterSeconds = parseRetryAfter(res.headers.get('Retry-After'));
  return {
    ok: false,
    code: mapResendCodeToI18n(code, res.status),
    retryAfterSeconds,
  };
}

// --- code-mapping helpers ------------------------------------------------

/** mapVerifyCodeToI18n routes the gateway's NNN_xxx code into the AC2 i18n keyset. */
function mapVerifyCodeToI18n(code: string, httpStatus: number): string {
  switch (code) {
    case '400_invalid_token':
      return 'auth.errors.invalidToken';
    case '410_token_expired':
      return 'auth.errors.tokenExpired';
    case '410_token_used':
      return 'auth.errors.tokenAlreadyUsed';
    case '502_auth_svc_unavailable':
      return 'auth.errors.serverUnavailable';
  }
  if (httpStatus >= 500) return 'auth.errors.serverUnavailable';
  if (httpStatus === 400) return 'auth.errors.invalidToken';
  if (httpStatus === 410) return 'auth.errors.tokenExpired';
  return 'auth.errors.serverUnavailable';
}

/** mapResendCodeToI18n routes the resend 429 variants into the AC2 keys. */
function mapResendCodeToI18n(code: string, httpStatus: number): string {
  switch (code) {
    case '429_rate_limit_resend_ip':
      return 'auth.errors.tooManyResendAttempts';
    case '429_rate_limit_resend_email':
      return 'auth.errors.resendTooSoon';
    case '400_invalid_email':
      return 'auth.errors.invalidEmail';
    case '502_auth_svc_unavailable':
      return 'auth.errors.serverUnavailable';
  }
  if (httpStatus === 429) return 'auth.errors.tooManyResendAttempts';
  if (httpStatus >= 500) return 'auth.errors.serverUnavailable';
  return 'auth.errors.serverUnavailable';
}

/** pickString defensively reads a string field from an unknown record. */
function pickString(body: unknown, key: string): string {
  if (typeof body !== 'object' || body === null) return '';
  const v = (body as Record<string, unknown>)[key];
  return typeof v === 'string' ? v : '';
}

// --- resendVerificationForm (form-action wrapper) ------------------------

/**
 * Form-action wrapper used by the ResendModal client component. Same
 * (prevState, formData) → state shape as `registerUserForm`. The form
 * is bound from the verify-email error-state page + signup check-inbox
 * page.
 */
export async function resendVerificationForm(
  _prev: ResendVerificationResult | null,
  formData: FormData,
): Promise<ResendVerificationResult> {
  return resendVerification({ email: String(formData.get('email') ?? '') });
}

export async function signinAction(_input: SigninInput): Promise<never> {
  signinSchema.parse(_input);
  throw new NotYetImplemented('signinAction', 'P4 (Story 2.2 T3, AC3)');
}
