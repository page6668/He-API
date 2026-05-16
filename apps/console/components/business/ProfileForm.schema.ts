// Story 2.5 — Profile form Zod schema (client + server validation parity).
//
// Mirrors apps/auth-svc/internal/handlers/update_profile.go validation:
//   - display_name: 0..100 runes after NFC normalisation; allowed code-point
//     classes Letter/Mark/Number/Punctuation/Currency-Symbol + single ASCII
//     space; empty after trim → clear (we pass empty string up the stack;
//     auth-svc normalises to NULL per BR-2.4).
//   - locale: one of the 10 MVP locales (Story 2.1 set).
//   - timezone: any string acceptable to Intl.DateTimeFormat (browser-side
//     check; server re-validates via Go time.LoadLocation).
//
// All `.optional()` fields participate in BR-2.1 partial-update — undefined
// means "do not change".

import { z } from 'zod';
import { locales } from '@/i18n/config';

// Character-class allowlist matching the Go isAllowedDisplayNameRune.
// \p{L} letters, \p{M} marks, \p{N} numbers, \p{P} punctuation, \p{Sc}
// currency symbols; space allowed. Reject everything else (so emoji
// (Symbol-Other) is rejected). Empty string is allowed because BR-2.4
// "empty → clear" is a valid intent.
const displayNameRegex = /^[\p{L}\p{M}\p{N}\p{P}\p{Sc} ]*$/u;

export const profileFormSchema = z.object({
  display_name: z
    .string()
    .trim()
    .max(100, 'account.profile.errors.display_name.too_long')
    .regex(displayNameRegex, 'account.profile.errors.display_name.invalid_chars')
    .optional(),
  locale: z
    .enum(locales, { errorMap: () => ({ message: 'account.profile.errors.locale.unsupported' }) })
    .optional(),
  timezone: z
    .string()
    .min(1, 'account.profile.errors.timezone.unknown')
    .refine(
      (tz) => {
        try {
          // Smoke-test the timezone via Intl. Catches IANA-invalid strings.
          new Intl.DateTimeFormat(undefined, { timeZone: tz });
          return true;
        } catch {
          return false;
        }
      },
      { message: 'account.profile.errors.timezone.unknown' },
    )
    .optional(),
});

export type ProfileFormValues = z.infer<typeof profileFormSchema>;
