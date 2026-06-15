// Story 10.7 AC2 — client-side Intercom Messenger control (graceful, zero-PII).
//
// Thin wrapper over the global `window.Intercom` the official snippet installs.
// Every call is defensive: if the widget script is blocked / fails to load /
// is absent, these become no-ops — a customer-support SaaS dependency must
// NEVER break the console (BR-10.7.10 / BLIND-ERROR-010). This module holds NO
// secret and sends NO PII; payloads come from the allow-listed builders.

import { buildSupportSessionUpdate, type IntercomBootPayload } from './boot-payload';

type IntercomFn = (...args: unknown[]) => void;

interface IntercomWindow extends Window {
  Intercom?: IntercomFn;
  intercomSettings?: Record<string, unknown>;
}

function getWin(): IntercomWindow | null {
  return typeof window === 'undefined' ? null : (window as IntercomWindow);
}

/**
 * Inject the standard Intercom loader once. The snippet defines `window.Intercom`
 * synchronously as a queue, so boot/update work before the remote widget script
 * resolves. Any failure is swallowed (graceful degrade — no crash).
 */
export function ensureIntercomLoaded(appId: string): void {
  const w = getWin();
  if (!w) return;
  try {
    if (typeof w.Intercom === 'function') return; // already present or queued
    const queue: unknown[][] = [];
    const shim: IntercomFn = (...args: unknown[]) => {
      queue.push(args);
    };
    (shim as unknown as { q: unknown[][] }).q = queue;
    w.Intercom = shim;

    const d = w.document;
    const s = d.createElement('script');
    s.type = 'text/javascript';
    s.async = true;
    s.src = `https://widget.intercom.io/widget/${appId}`;
    const first = d.getElementsByTagName('script')[0];
    if (first?.parentNode) {
      first.parentNode.insertBefore(s, first);
    } else {
      d.head?.appendChild(s);
    }
  } catch {
    /* never break the host app (BLIND-ERROR-010) */
  }
}

function call(method: string, arg?: unknown): void {
  const w = getWin();
  if (!w || typeof w.Intercom !== 'function') return; // graceful degrade
  try {
    if (arg === undefined) w.Intercom(method);
    else w.Intercom(method, arg);
  } catch {
    /* swallow — the widget must never break the console */
  }
}

export function bootIntercom(payload: IntercomBootPayload): void {
  call('boot', payload);
}

export function updateIntercom(attrs: Record<string, unknown>): void {
  call('update', attrs);
}

export function shutdownIntercom(): void {
  call('shutdown');
}

/**
 * Bring a §11.5 `he_request_id` handle into the support session (BR-10.7.9,
 * front-end-spec:537). Returns false (without opening) when the handle is
 * malformed — the session is never blocked. The handle is the ONLY identifier
 * sent; it is opaque and non-PII.
 */
export function openSupportWithRequestId(requestId: string): boolean {
  const update = buildSupportSessionUpdate(requestId);
  if (!update) return false;
  updateIntercom({ he_request_id: update.he_request_id });
  call('showNewMessage', '');
  return true;
}
