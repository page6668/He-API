/**
 * Shared E2E helpers for Story 2.2 specs.
 *
 * These wrap the test-environment dependencies (mailpit, HIBP stub) so
 * individual specs stay declarative. The helper module is the single
 * place to update if the E2E test infra evolves (e.g., switching from
 * mailpit to MailHog or moving the HIBP stub from WireMock to a custom
 * fake).
 *
 * Required env vars (set in CI):
 *   MAILPIT_URL      — default http://localhost:8025
 *   HIBP_STUB_URL    — default http://localhost:8081 (WireMock)
 *   GATEWAY_URL      — default http://localhost:8080
 */

import type { APIRequestContext } from '@playwright/test';

export const MAILPIT_URL = process.env.MAILPIT_URL ?? 'http://localhost:8025';
export const HIBP_STUB_URL = process.env.HIBP_STUB_URL ?? 'http://localhost:8081';
export const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://localhost:8080';

/**
 * Generates a fresh test email so parallel test workers don't collide
 * on the users.email UNIQUE index. Uses the test name prefix + a
 * timestamp + a random suffix.
 */
export function freshEmail(prefix = 'e2e'): string {
  const ts = Date.now();
  const rnd = Math.random().toString(36).slice(2, 8);
  return `${prefix}-${ts}-${rnd}@he-api-test.example.com`;
}

/** Mirrors the console's _actions/auth.ts maskEmail — keep aligned. */
export function expectedMaskedEmail(email: string): string {
  const at = email.indexOf('@');
  if (at <= 0) return email;
  const local = email.slice(0, at);
  const domain = email.slice(at);
  if (local.length <= 1) return `${local}*${domain}`;
  const visible = local[0];
  const maskedCount = Math.min(local.length - 1, 6);
  return `${visible}${'*'.repeat(maskedCount)}${domain}`;
}

/* ------------------------------------------------------------------ */
/* Mailpit                                                             */
/* ------------------------------------------------------------------ */

interface MailpitMessage {
  ID: string;
  Subject: string;
  To: Array<{ Address: string }>;
}

interface MailpitListResponse {
  messages: MailpitMessage[];
  total: number;
}

interface MailpitMessageDetail {
  ID: string;
  Subject: string;
  HTML: string;
  Text: string;
  Date: string;
}

/**
 * Wait up to `timeoutMs` for mailpit to surface at least one message
 * addressed to `email`. Polls every 500ms. Returns the most recent
 * matching message detail. Fails the test if the timeout elapses.
 *
 * Uses mailpit's REST query API:
 *   GET /api/v1/messages?query=to:<email>
 *   GET /api/v1/message/<id>
 */
export async function waitForVerificationEmail(
  request: APIRequestContext,
  email: string,
  timeoutMs = 10_000,
): Promise<MailpitMessageDetail> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const list = await request.get(
      `${MAILPIT_URL}/api/v1/messages?query=${encodeURIComponent(`to:${email}`)}`,
    );
    if (list.ok()) {
      const body = (await list.json()) as MailpitListResponse;
      if (body.messages.length > 0) {
        const id = body.messages[0].ID;
        const detail = await request.get(`${MAILPIT_URL}/api/v1/message/${id}`);
        if (detail.ok()) {
          return (await detail.json()) as MailpitMessageDetail;
        }
      }
    }
    await sleep(500);
  }
  throw new Error(`mailpit: no message to ${email} within ${timeoutMs}ms`);
}

/**
 * Counts messages addressed to `email`. Used by BR-1.4 anti-enumeration
 * tests that assert NO new email is sent when a duplicate signs up.
 */
export async function countEmails(
  request: APIRequestContext,
  email: string,
): Promise<number> {
  const list = await request.get(
    `${MAILPIT_URL}/api/v1/messages?query=${encodeURIComponent(`to:${email}`)}`,
  );
  if (!list.ok()) return 0;
  const body = (await list.json()) as MailpitListResponse;
  return body.total ?? body.messages.length;
}

/**
 * Extracts the verification URL from an email body. The HTML body
 * carries `https://console.<env>/<locale>/verify-email?token=<64chars>`.
 */
export function extractVerifyURL(email: MailpitMessageDetail): string {
  const bodies = [email.HTML, email.Text].filter((b) => typeof b === 'string');
  const re = /(https?:\/\/[^\s"'<>]+\/verify-email\?token=[A-Za-z0-9_-]{64})/;
  for (const body of bodies) {
    const m = body.match(re);
    if (m) return m[1];
  }
  throw new Error('verify URL not found in email body');
}

/** Pulls just the token query parameter — useful for tampered-token tests. */
export function extractVerifyToken(email: MailpitMessageDetail): string {
  const url = extractVerifyURL(email);
  const u = new URL(url);
  const token = u.searchParams.get('token') ?? '';
  if (token.length !== 64) {
    throw new Error(`token length ${token.length}, expected 64`);
  }
  return token;
}

/* ------------------------------------------------------------------ */
/* HIBP stub (WireMock)                                                */
/* ------------------------------------------------------------------ */

/**
 * Resets the HIBP WireMock stub between tests so per-test scenario
 * mappings (e.g. "respond 503 on next request") don't leak. No-op
 * when the stub doesn't expose the WireMock admin API.
 */
export async function resetHibpStub(request: APIRequestContext): Promise<void> {
  try {
    await request.post(`${HIBP_STUB_URL}/__admin/reset`, { timeout: 2000 });
  } catch {
    // Stub not WireMock-compatible — skip silently.
  }
}

/** Configures the HIBP stub to return 503 on the next probe (INT-003). */
export async function stubHibpUnavailable(request: APIRequestContext): Promise<void> {
  await request.post(`${HIBP_STUB_URL}/__admin/mappings`, {
    data: {
      request: { method: 'GET', urlPathPattern: '/range/.+' },
      response: { status: 503, body: 'service unavailable' },
    },
  });
}

/* ------------------------------------------------------------------ */
/* Misc                                                                */
/* ------------------------------------------------------------------ */

export function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
