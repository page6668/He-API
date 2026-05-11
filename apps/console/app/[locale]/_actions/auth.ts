'use server';

// Auth Server Actions (Story 2.2 T0.11 scaffold).
//
// P1 ships the zod-validated entry shape + the upstream URL plan. Real fetch
// against api-gateway + redirect chain + error-code translation lands per
// phase:
//
//   registerUser       → P2 (T1, AC1)
//   verifyEmailAction  → P3 (T2, AC2)
//   resendVerification → P3 (T2, AC2)
//   signinAction       → P4 (T3, AC3)
//
// Each action validates input via zod (BR-1.8 + parity with auth-svc), then
// would post to api-gateway under /v1/auth/* (Wright Round 1 Q1 ruling).
// During P1 every action throws so accidental wiring fails loudly rather than
// silently no-op'ing.

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

export async function registerUser(_input: SignupInput): Promise<never> {
  signupSchema.parse(_input);
  throw new NotYetImplemented('registerUser', 'P2 (Story 2.2 T1, AC1)');
}

export async function verifyEmailAction(_input: VerifyEmailTokenInput): Promise<never> {
  verifyEmailTokenSchema.parse(_input);
  throw new NotYetImplemented('verifyEmailAction', 'P3 (Story 2.2 T2, AC2)');
}

export async function resendVerification(_input: ResendVerificationInput): Promise<never> {
  resendVerificationSchema.parse(_input);
  throw new NotYetImplemented('resendVerification', 'P3 (Story 2.2 T2, AC2)');
}

export async function signinAction(_input: SigninInput): Promise<never> {
  signinSchema.parse(_input);
  throw new NotYetImplemented('signinAction', 'P4 (Story 2.2 T3, AC3)');
}
