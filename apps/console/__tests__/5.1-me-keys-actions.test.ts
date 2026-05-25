/**
 * Story 5.1 T7.4 — Vitest tests for the Console BFF Server Actions
 * (createMyKey / listMyKeys / revokeMyKey).
 *
 * Each test mocks the global fetch to assert the action's request shape
 * + response mapping. Cookies are mocked via vi.mock('next/headers').
 *
 * Plaintext invariants (BR-1.5): when the gateway returns 201 with a
 * plaintext field, the action forwards it ONCE in its return value.
 * These tests assert the forwarding contract but do NOT log the plaintext
 * (the action code itself enforces no-log).
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('next/headers', () => ({
  cookies: vi.fn(async () => ({
    getAll: () => [
      { name: 'he_access', value: 'access-jwt-stub' },
      { name: 'he_csrf', value: 'csrf-stub' },
    ],
  })),
}));

// Late import so the vi.mock above resolves first.
import { createMyKey } from '@/app/[locale]/(console)/keys/_actions/create-key';
import { listMyKeys } from '@/app/[locale]/(console)/keys/_actions/list-keys';
import { revokeMyKey } from '@/app/[locale]/(console)/keys/_actions/revoke-key';

const stubBaseURL = 'http://test-gateway:8080';

function withFetch(fn: typeof fetch) {
  global.fetch = vi.fn(fn) as unknown as typeof fetch;
}

beforeEach(() => {
  process.env.HE_API_GATEWAY_URL = stubBaseURL;
});

describe('createMyKey', () => {
  it('returns success with plaintext on 201', async () => {
    const fakeResponse = {
      api_key_id: '00000000-0000-4000-8000-0000000000aa',
      key_prefix: 'he-AAA111222',
      name: 'My First Key',
      plaintext: 'he-AAA111222ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ',
      created_at: '2026-05-25T12:00:00Z',
      warning: 'This plaintext is shown ONCE. Store it now — it cannot be retrieved later.',
    };
    withFetch(async (input, init) => {
      expect(String(input)).toBe(`${stubBaseURL}/v1/me/keys`);
      expect(init?.method).toBe('POST');
      const body = JSON.parse(String(init?.body));
      expect(body).toEqual({ name: 'My First Key' });
      // Cookies + Origin forwarded
      const headers = init?.headers as Record<string, string>;
      expect(headers['Cookie']).toContain('he_access=access-jwt-stub');
      expect(headers['Origin']).toBeTruthy();
      return new Response(JSON.stringify(fakeResponse), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await createMyKey({ name: 'My First Key' });
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.response.plaintext).toBe(fakeResponse.plaintext);
      expect(result.response.api_key_id).toBe(fakeResponse.api_key_id);
    }
  });

  it('rejects empty name BEFORE calling the gateway', async () => {
    let fetchCalled = false;
    withFetch(async () => {
      fetchCalled = true;
      return new Response('', { status: 500 });
    });
    const result = await createMyKey({ name: '' });
    expect(result.ok).toBe(false);
    expect(fetchCalled).toBe(false);
    if (!result.ok) {
      expect(result.error.code).toMatch(/account\.keys\.errors\.name/);
    }
  });

  it('maps 429 with Retry-After to rate_limited error', async () => {
    withFetch(async () => {
      return new Response(
        JSON.stringify({ error: { code: '429_rate_limit_key_create' } }),
        { status: 429, headers: { 'Retry-After': '120', 'Content-Type': 'application/json' } },
      );
    });
    const result = await createMyKey({ name: 'X' });
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.code).toBe('account.keys.errors.rate_limited');
      expect(result.error.retryAfterSeconds).toBe(120);
    }
  });

  it('returns generic error on network failure', async () => {
    global.fetch = vi.fn(async () => {
      throw new Error('network down');
    }) as unknown as typeof fetch;
    const result = await createMyKey({ name: 'X' });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.errors.generic');
  });
});

describe('listMyKeys', () => {
  it('returns parsed ListKeysResponse on 200', async () => {
    const fakeBody = {
      object: 'list',
      data: [
        {
          api_key_id: '00000000-0000-4000-8000-0000000000aa',
          name: 'K1',
          key_prefix: 'he-AAA111222',
          scope: {},
          monthly_cost_cap_usd: null,
          current_month_cost_usd: '0',
          last_used_at: null,
          revoked_at: null,
          created_at: '2026-05-25T12:00:00Z',
        },
      ],
    };
    withFetch(async (input, init) => {
      expect(String(input)).toBe(`${stubBaseURL}/v1/me/keys`);
      expect(init?.method).toBe('GET');
      return new Response(JSON.stringify(fakeBody), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await listMyKeys();
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.response.object).toBe('list');
      expect(result.response.data).toHaveLength(1);
      expect(result.response.data[0]?.key_prefix).toBe('he-AAA111222');
    }
  });

  it('degrades to empty list on shape drift', async () => {
    withFetch(async () => {
      return new Response(JSON.stringify({ unexpected: 'shape' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await listMyKeys();
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.response.data).toHaveLength(0);
    }
  });

  it('returns generic error on 503', async () => {
    withFetch(async () => {
      return new Response(JSON.stringify({ error: { code: '503_auth_unavailable' } }), {
        status: 503,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await listMyKeys();
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.errors.generic');
  });
});

describe('revokeMyKey', () => {
  it('returns parsed RevokeKeyResponse on 200', async () => {
    const fakeBody = {
      api_key_id: '00000000-0000-4000-8000-0000000000aa',
      revoked_at: '2026-05-25T12:05:00Z',
      was_already_revoked: false,
    };
    withFetch(async (input, init) => {
      expect(String(input)).toBe(
        `${stubBaseURL}/v1/me/keys/00000000-0000-4000-8000-0000000000aa`,
      );
      expect(init?.method).toBe('DELETE');
      return new Response(JSON.stringify(fakeBody), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await revokeMyKey({ api_key_id: '00000000-0000-4000-8000-0000000000aa' });
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.response.was_already_revoked).toBe(false);
    }
  });

  it('rejects bad UUID BEFORE calling the gateway', async () => {
    let fetchCalled = false;
    withFetch(async () => {
      fetchCalled = true;
      return new Response('', { status: 500 });
    });
    const result = await revokeMyKey({ api_key_id: 'not-a-uuid' });
    expect(result.ok).toBe(false);
    expect(fetchCalled).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.revoke.not_found');
  });

  it('maps 404 envelope to not_found i18n key', async () => {
    withFetch(async () => {
      return new Response(JSON.stringify({ error: { code: '404_api_key_not_found' } }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await revokeMyKey({ api_key_id: '00000000-0000-4000-8000-0000000000bb' });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.revoke.not_found');
  });
});
