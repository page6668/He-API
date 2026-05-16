'use server';

// Story 2.5 AC2 — Console BFF Server Action: updateMyProfile.
//
// Sends a PUT /v1/me/profile with the (optionally partial) body + If-Match
// etag. On 200 with locale change: revalidatePath + redirect to the new
// locale's URL so next-intl renders the new locale on the very next paint
// (AC3 — the headline AC).
//
// Returns a discriminated-union result for the form to render. On the happy
// path with locale change, this function throws the Next.js redirect signal
// (TypeScript "never" return) — the form's catch handles all other branches.

import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { revalidatePath } from 'next/cache';
import { z } from 'zod';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';
import { locales, isLocale } from '@/i18n/config';
import { profileFormSchema, type ProfileFormValues } from '@/components/business/ProfileForm.schema';

// Wire response — mirrors getMyProfile + adds `locale_changed`.
const updateProfileResponseSchema = z.object({
  user_id: z.string(),
  email: z.string().email(),
  display_name: z.string().nullable(),
  locale: z.enum(locales),
  timezone: z.string(),
  totp_enabled: z.boolean(),
  oauth_provider: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
});

export type UpdateMyProfileResult =
  | { kind: 'ok'; localeChanged: false; etag: string }
  | { kind: 'concurrent_update' }
  | { kind: 'validation'; fieldErrors: Partial<Record<keyof ProfileFormValues, string>> }
  | { kind: 'rate_limited'; retryAfter: number }
  | { kind: 'unauthorized' }
  | { kind: 'pending_deletion' }
  | { kind: 'error' };

interface UpdateMyProfileInput {
  values: ProfileFormValues;
  ifMatch: string;
  currentLocale: string; // the URL segment locale; drives the redirect destination
}

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function updateMyProfile(input: UpdateMyProfileInput): Promise<UpdateMyProfileResult> {
  // Client-side Zod parity (defence-in-depth — server validates too).
  const parsed = profileFormSchema.safeParse(input.values);
  if (!parsed.success) {
    const fieldErrors: Partial<Record<keyof ProfileFormValues, string>> = {};
    for (const issue of parsed.error.issues) {
      const key = issue.path[0] as keyof ProfileFormValues;
      fieldErrors[key] = issue.message; // already an i18n key
    }
    return { kind: 'validation', fieldErrors };
  }

  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  // BR-2.1 — partial body: only include fields the user actually set.
  // display_name === "" means "clear" (BR-2.4); we still include it.
  const body: Record<string, string | null> = {};
  if (parsed.data.display_name !== undefined) body.display_name = parsed.data.display_name;
  if (parsed.data.locale !== undefined) body.locale = parsed.data.locale;
  if (parsed.data.timezone !== undefined) body.timezone = parsed.data.timezone;

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/me/profile`, {
      method: 'PUT',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        'Content-Type': 'application/json',
        'If-Match': input.ifMatch,
        Accept: 'application/json',
      },
      body: JSON.stringify(body),
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'pending_deletion' };
  if (res.status === 412) return { kind: 'concurrent_update' };
  if (res.status === 428) return { kind: 'concurrent_update' }; // missing If-Match also surfaced as concurrent
  if (res.status === 429) {
    const retry = parseInt(res.headers.get('retry-after') ?? '60', 10);
    return { kind: 'rate_limited', retryAfter: Number.isFinite(retry) ? retry : 60 };
  }
  if (res.status === 400) {
    // Field-level validation rejection — surface as generic to the form;
    // future enhancement: parse `error.code` to per-field key.
    return { kind: 'validation', fieldErrors: {} };
  }
  if (!res.ok) return { kind: 'error' };

  const raw = await res.json();
  const respParsed = updateProfileResponseSchema.safeParse(raw);
  if (!respParsed.success) return { kind: 'error' };

  const newLocale = respParsed.data.locale;
  const oldLocale = input.currentLocale;

  // BR-3.10 / AC3 — server-driven locale redirect. Throws the Next.js
  // redirect signal; the caller's try/catch must re-throw NEXT_REDIRECT.
  if (newLocale !== oldLocale && isLocale(newLocale)) {
    revalidatePath(`/${newLocale}/settings/profile`);
    redirect(`/${newLocale}/settings/profile`);
  }

  revalidatePath(`/${oldLocale}/settings/profile`);
  return { kind: 'ok', localeChanged: false, etag: res.headers.get('etag') ?? '' };
}
