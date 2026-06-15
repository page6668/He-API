/**
 * Story 10.6 — Playground Playwright E2E (AC1).
 *
 * Self-contained: the JWT-cookie-authed gateway call POST /v1/me/playground/chat
 * (and the /v1/me/keys list) are intercepted via `page.route` — the billed call is
 * ALWAYS mocked, NEVER burning real quota (T4.2). The page renders for the test
 * with a mocked key set; the security guarantees of the real endpoint are covered
 * at the gateway integration layer (playground_chat_test.go, 19 tests).
 *
 *   10.6-E2E-001 / INT-019 / INT-020   single-model send → streaming output + metrics + estimated cost
 *   10.6-E2E-002 / INT-021             A/B dual output (non-stream) + per-leg attribution
 *   10.6-INT-022                       front-end never sends stream=true with an A/B header
 *   10.6-E2E-003                       Export 4 languages copyable
 *   10.6-E2E-004                       deep-link #fragment prefill (literal text, never eval)
 *   10.6-E2E-005 (P2)                  /ar/playground RTL — code/snippet stay LTR islands
 *   10.6-INT-018                       unauthenticated deep-link landing → sign-in redirect preserves the deep link
 *   10.6-BLIND-ERROR-001 (P0)          SSE mid-interrupt → graceful (partial output, no crash)
 *   10.6-BLIND-FLOW-002                double-click send → no duplicate billed call
 */
import { test, expect, type Page } from '@playwright/test';

const KEY_ID = '33333333-3333-4333-8333-333333333333';

async function mockKeys(page: Page, status = 200) {
  await page.route('**/v1/me/keys', (route) =>
    status === 200
      ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ object: 'list', data: [{ api_key_id: KEY_ID, name: 'pg-key' }] }) })
      : route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ error: { code: '401_unauthenticated' } }) }),
  );
}

const SSE_BODY =
  'data: {"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}\n\n' +
  'data: {"choices":[{"delta":{"content":" pandas"}}]}\n\n' +
  'data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}\n\n' +
  'data: [DONE]\n\n';

test.describe('AC1: Playground', () => {
  test('10.6-E2E-001 / INT-019 / INT-020: single-model send → streaming output + metrics + estimated cost', async ({ page }) => {
    await mockKeys(page);
    let sentBody: Record<string, unknown> = {};
    await page.route('**/v1/me/playground/chat', (route) => {
      sentBody = JSON.parse(route.request().postData() ?? '{}');
      route.fulfill({ status: 200, contentType: 'text/event-stream; charset=utf-8', body: SSE_BODY });
    });

    await page.goto('/en/playground');
    await page.getByTestId('playground-user').fill('Tell me a fun fact about pandas');
    await page.getByTestId('playground-send').click();

    await expect(page.getByTestId('playground-output')).toContainText('Hello pandas');
    await expect(page.getByTestId('playground-metrics')).toBeVisible();
    // estimated cost shown + labeled (never a billed amount).
    await expect(page.getByTestId('playground-cost')).toContainText('$');
    await expect(page.getByTestId('playground-cost')).toContainText('estimated');
    // INT-019 — request carried the owned api_key_id (browser holds no plaintext key).
    expect(sentBody.api_key_id).toBe(KEY_ID);
  });

  test('10.6-E2E-002 / INT-021: A/B dual output (non-stream) + per-leg attribution', async ({ page }) => {
    await mockKeys(page);
    await page.route('**/v1/me/playground/chat', (route) => {
      const body = {
        object: 'chat.completion',
        model: 'qwen-max',
        choices: [
          { index: 0, message: { content: 'A!' }, finish_reason: 'stop', x_he_model: 'qwen-max' },
          { index: 1, message: { content: 'B!' }, finish_reason: 'stop', x_he_model: 'deepseek-v3' },
        ],
        usage: { prompt_tokens: 10, completion_tokens: 20, total_tokens: 30 },
      };
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });

    await page.goto('/en/playground');
    await page.getByTestId('playground-ab-toggle').check();
    await page.getByTestId('playground-modelB').selectOption('deepseek-v3');
    await page.getByTestId('playground-user').fill('compare these');
    await page.getByTestId('playground-send').click();

    const out = page.getByTestId('playground-output');
    await expect(out).toContainText('[qwen-max] A!');
    await expect(out).toContainText('[deepseek-v3] B!');
  });

  test('10.6-INT-022: A/B never sends stream=true (non-stream only) + sends the A/B header', async ({ page }) => {
    await mockKeys(page);
    let sentBody: Record<string, unknown> = {};
    let abHeader: string | undefined;
    await page.route('**/v1/me/playground/chat', (route) => {
      sentBody = JSON.parse(route.request().postData() ?? '{}');
      abHeader = route.request().headers()['x-he-ab-models'];
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ choices: [], usage: {} }) });
    });
    await page.goto('/en/playground');
    await page.getByTestId('playground-ab-toggle').check();
    await page.getByTestId('playground-modelB').selectOption('deepseek-v3');
    await page.getByTestId('playground-user').fill('x');
    await page.getByTestId('playground-send').click();
    await expect.poll(() => abHeader).toBe('qwen-max,deepseek-v3');
    expect(sentBody.stream).toBeUndefined();
  });

  test('10.6-E2E-003: Export 4 languages copyable', async ({ page }) => {
    await mockKeys(page);
    await page.goto('/en/playground');
    await page.getByTestId('playground-user').fill('hi');

    await page.getByTestId('playground-export-curl').click();
    await expect(page.getByTestId('playground-snippet')).toContainText('/v1/chat/completions');
    await page.getByTestId('playground-export-python').click();
    await expect(page.getByTestId('playground-snippet')).toContainText('from he_api import Client');
    await page.getByTestId('playground-export-typescript').click();
    await expect(page.getByTestId('playground-snippet')).toContainText('@he-api/sdk');
    await page.getByTestId('playground-export-go').click();
    await expect(page.getByTestId('playground-snippet')).toContainText('heapi.NewClient()');
  });

  test('10.6-E2E-004: deep-link #fragment prefill (literal text, never eval)', async ({ page }) => {
    await mockKeys(page);
    const payload = { model: 'deepseek-v3', user: 'process.exit(1) // literal', temperature: 0.5 };
    const frag = `#prefill=${encodeURIComponent(JSON.stringify(payload))}`;
    await page.goto(`/en/playground${frag}`);
    await expect(page.getByTestId('playground-user')).toHaveValue('process.exit(1) // literal');
    await expect(page.getByTestId('playground-model')).toHaveValue('deepseek-v3');
    await expect(page.getByTestId('playground-notice')).toBeVisible();
  });

  test('10.6-E2E-005: /ar/playground RTL — code/snippet stay LTR islands', async ({ page }) => {
    await mockKeys(page);
    await page.goto('/ar/playground');
    await expect(page.getByTestId('playground')).toHaveAttribute('dir', 'rtl');
    await expect(page.getByTestId('playground-snippet')).toHaveAttribute('dir', 'ltr');
  });

  test('10.6-INT-018: unauthenticated deep-link landing → sign-in redirect preserves the deep link', async ({ page }) => {
    await mockKeys(page, 401);
    const frag = `#prefill=${encodeURIComponent(JSON.stringify({ model: 'qwen-max', user: 'hi' }))}`;
    await page.goto(`/en/playground${frag}`);
    await page.waitForURL(/\/en\/signin\?return_to=/);
    expect(decodeURIComponent(page.url())).toContain('/en/playground#prefill=');
  });

  test('10.6-BLIND-ERROR-001: SSE mid-interrupt → graceful partial output, no crash', async ({ page }) => {
    await mockKeys(page);
    // a truncated stream (no [DONE], no usage) — the client must not crash.
    const truncated = 'data: {"choices":[{"delta":{"content":"partial"}}]}\n\n';
    await page.route('**/v1/me/playground/chat', (route) =>
      route.fulfill({ status: 200, contentType: 'text/event-stream; charset=utf-8', body: truncated }),
    );
    await page.goto('/en/playground');
    await page.getByTestId('playground-user').fill('hi');
    await page.getByTestId('playground-send').click();
    await expect(page.getByTestId('playground-output')).toContainText('partial');
    // page still interactive (no crash): send button is re-enabled.
    await expect(page.getByTestId('playground-send')).toBeEnabled();
  });

  test('10.6-BLIND-FLOW-002: double-click send → no duplicate billed call', async ({ page }) => {
    await mockKeys(page);
    let calls = 0;
    await page.route('**/v1/me/playground/chat', async (route) => {
      calls += 1;
      await new Promise((r) => setTimeout(r, 200)); // hold so the 2nd click lands while sending
      route.fulfill({ status: 200, contentType: 'text/event-stream; charset=utf-8', body: SSE_BODY });
    });
    await page.goto('/en/playground');
    await page.getByTestId('playground-user').fill('hi');
    const send = page.getByTestId('playground-send');
    await send.click();
    await send.click({ force: true }).catch(() => {}); // disabled while sending → no second call
    await expect(page.getByTestId('playground-output')).toContainText('Hello pandas');
    expect(calls).toBe(1);
  });
});
