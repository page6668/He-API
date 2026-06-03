/**
 * Story 5.2 T6.2 — Vitest tests for the updateMyKey Server Action.
 *
 * Mocks global fetch to assert the PATCH request shape + response/error
 * mapping. Cookies mocked via vi.mock('next/headers'). This test is the
 * in-Story consumer of the action (Dev-Gate §14 deliverable binding).
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

import { updateMyKey } from '@/app/[locale]/(console)/keys/_actions/update-key';

const stubBaseURL = 'http://test-gateway:8080';
const validID = '01234567-89ab-4def-8123-456789abcdef';

function withFetch(fn: typeof fetch) {
  global.fetch = vi.fn(fn) as unknown as typeof fetch;
}

beforeEach(() => {
  process.env.HE_API_GATEWAY_URL = stubBaseURL;
});

describe('updateMyKey', () => {
  it('PATCHes the patch body and returns the validated response on 200', async () => {
    const fakeResponse = {
      api_key_id: validID,
      name: 'Production',
      key_prefix: 'he-AA1BB2CC3',
      scope: { models: ['qwen-max'] },
      monthly_cost_cap_usd: '50.00',
      current_month_cost_usd: '0',
      last_used_at: null,
      revoked_at: null,
      created_at: '2026-06-03T12:00:00Z',
    };
    withFetch(async (input, init) => {
      expect(String(input)).toBe(`${stubBaseURL}/v1/me/keys/${validID}`);
      expect(init?.method).toBe('PATCH');
      const body = JSON.parse(String(init?.body));
      expect(body).toEqual({ scope: { models: ['qwen-max'] }, monthly_cost_cap_usd: '50.00' });
      const headers = init?.headers as Record<string, string>;
      expect(headers['Cookie']).toContain('he_access=access-jwt-stub');
      expect(headers['Content-Type']).toBe('application/json');
      return new Response(JSON.stringify(fakeResponse), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    const result = await updateMyKey({
      api_key_id: validID,
      patch: { scope: { models: ['qwen-max'] }, monthly_cost_cap_usd: '50.00' },
    });
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.response.monthly_cost_cap_usd).toBe('50.00');
      expect(result.response.api_key_id).toBe(validID);
    }
  });

  it('rejects a non-UUID id BEFORE calling the gateway', async () => {
    let called = false;
    withFetch(async () => {
      called = true;
      return new Response('{}', { status: 200 });
    });
    const result = await updateMyKey({ api_key_id: 'nope', patch: {} });
    expect(called).toBe(false);
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.edit.errors.not_found');
  });

  it('maps 404 → not_found i18n key', async () => {
    withFetch(
      async () =>
        new Response(JSON.stringify({ error: { code: '404_api_key_not_found' } }), {
          status: 404,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const result = await updateMyKey({ api_key_id: validID, patch: { monthly_cost_cap_usd: null } });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.edit.errors.not_found');
  });

  it('maps 403 pending_deletion → account_pending_deletion i18n key', async () => {
    withFetch(
      async () =>
        new Response(JSON.stringify({ error: { code: '403_account_pending_deletion' } }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const result = await updateMyKey({ api_key_id: validID, patch: { monthly_cost_cap_usd: '10.00' } });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.edit.errors.account_pending_deletion');
  });

  it('maps 400_invalid_request → invalid_request i18n key', async () => {
    withFetch(
      async () =>
        new Response(JSON.stringify({ error: { code: '400_invalid_request' } }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const result = await updateMyKey({
      api_key_id: validID,
      patch: { scope: { ip_whitelist: ['0.0.0.0/0'] } },
    });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.edit.errors.invalid_request');
  });

  it('maps a network error → generic i18n key', async () => {
    withFetch(async () => {
      throw new Error('ECONNREFUSED');
    });
    const result = await updateMyKey({ api_key_id: validID, patch: { monthly_cost_cap_usd: '10.00' } });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error.code).toBe('account.keys.edit.errors.generic');
  });
});
