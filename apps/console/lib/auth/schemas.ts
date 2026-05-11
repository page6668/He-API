// Auth zod schemas (Story 2.2 T0.11 — console BFF input validation).
//
// Schemas mirror the server-side validation that auth-svc applies (BR-1.1,
// BR-1.3, BR-3.10): client + server both validate; identical rejections.
//
// Imports stay flat (no path aliases) so this module can be consumed from
// Server Actions and Route Handlers alike.

import { z } from 'zod';
import { locales } from '@/i18n/config';

// Email — RFC 5322 simplified (BR-1.1): length ≤ 254, local-part ≤ 64,
// must contain `@` and at least one `.`. zod's `.email()` is permissive on
// length; we layer length bounds explicitly.
const emailField = z
  .string()
  .trim()
  .toLowerCase()
  .min(3)
  .max(254)
  .email()
  .refine((s) => {
    const at = s.indexOf('@');
    return at > 0 && at <= 64;
  });

// Password length boundary (BR-1.3, NIST SP 800-63B §5.1.1.2). No
// character-class enforcement. The HaveIBeenPwned breach check is a
// server-side step (auth-svc internal/password) and is not duplicated here.
const passwordSignupField = z.string().min(10).max(1024);

// Sign-in only validates non-emptiness (auth-svc internal/password compares).
const passwordSigninField = z.string().min(1).max(1024);

// Locale must match the Story 2.1 const-tuple.
const localeField = z.enum(locales);

// Verification token — base64url, 64 chars (32-byte CSPRNG, base64url-encoded).
const tokenField = z
  .string()
  .length(64)
  .regex(/^[A-Za-z0-9_-]+$/);

export const signupSchema = z.object({
  email: emailField,
  password: passwordSignupField,
  locale: localeField.optional(),
});

export const signinSchema = z.object({
  email: emailField,
  password: passwordSigninField,
});

export const resendVerificationSchema = z.object({
  email: emailField,
});

export const verifyEmailTokenSchema = z.object({
  token: tokenField,
});

export type SignupInput = z.infer<typeof signupSchema>;
export type SigninInput = z.infer<typeof signinSchema>;
export type ResendVerificationInput = z.infer<typeof resendVerificationSchema>;
export type VerifyEmailTokenInput = z.infer<typeof verifyEmailTokenSchema>;
