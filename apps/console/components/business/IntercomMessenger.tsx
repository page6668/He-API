'use client';

// Story 10.7 AC2 — Intercom Messenger mount (console authed face only, OQ-6).
//
// Boots the customer-support Messenger with a ZERO-PII payload (only app_id +
// language_override; no identity — OQ-2). Gated server-side: it only mounts
// inside the cookie-gated (console) layout (authed), and only boots when an
// app id is present and the request is NOT from a PRC region (region-gate,
// UNIT-013). Renders nothing — it is a side-effect-only controller.

import { useEffect, useRef } from 'react';

import { buildBootPayload } from '@/lib/intercom/boot-payload';
import { toLanguageOverride } from '@/lib/intercom/config';
import {
  bootIntercom,
  ensureIntercomLoaded,
  shutdownIntercom,
  updateIntercom,
} from '@/lib/intercom/messenger';
import { shouldBootIntercom } from '@/lib/intercom/region';

export interface IntercomMessengerProps {
  /** Public Intercom app id (NEXT_PUBLIC_INTERCOM_APP_ID); absent → no widget. */
  appId: string | undefined;
  /** Active console locale → Intercom language_override. */
  locale: string;
  /** Edge-resolved ISO country code (cf-ipcountry); PRC → no boot. */
  countryCode: string | null | undefined;
}

export function IntercomMessenger({ appId, locale, countryCode }: IntercomMessengerProps) {
  const booted = useRef(false);

  // Boot once when gating allows; shut down on unmount so a shared device does
  // not leak the previous user's conversation (BLIND-FLOW-010).
  useEffect(() => {
    if (!shouldBootIntercom({ appId, countryCode })) return;
    ensureIntercomLoaded(appId as string);
    bootIntercom(buildBootPayload({ appId: appId as string, locale }));
    booted.current = true;
    return () => {
      if (booted.current) {
        shutdownIntercom();
        booted.current = false;
      }
    };
    // locale is intentionally excluded — a locale change updates in place below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId, countryCode]);

  // Reflect a mid-session locale switch without a full reload (BLIND-FLOW-011).
  useEffect(() => {
    if (!booted.current) return;
    updateIntercom({ language_override: toLanguageOverride(locale) });
  }, [locale]);

  return null;
}
