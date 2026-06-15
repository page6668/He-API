/**
 * Hermetic test helpers for the @he-api/sdk suite.
 *
 * Not a test module (leading underscore — vitest only collects *.test.ts).
 *
 * All hermetic tests inject a mock `fetch` into the openai-node client
 * (R-OQ-10.3-5) — there is NO real gateway. Shape assertions mirror
 * apps/api-gateway/tests/_protocol_invariants.py (the drop-in oracle) so the
 * wrapper is proven shape-faithful in the same terms as the gateway's own
 * contract suite.
 */
import { expect } from 'vitest';

export const REQUEST_ID_RE = /^req_[0-9a-f]{12}$/;
const CHATCMPL_ID_RE = /^chatcmpl-/;
const CANONICAL_FINISH_REASONS = new Set(['stop', 'length', 'tool_calls', 'content_filter']);

// ---------------------------------------------------------------------------
// Mock fetch — capture outgoing requests, return programmed responses
// ---------------------------------------------------------------------------

export interface CapturedRequest {
  url: string;
  method?: string;
  headers: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
}

/** Normalise any HeadersInit shape openai-node may pass into a lowercased map. */
export function headersToObject(h: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!h) return out;
  if (typeof (h as Headers).forEach === 'function' && !Array.isArray(h)) {
    (h as Headers).forEach((v, k) => {
      out[String(k).toLowerCase()] = String(v);
    });
  } else if (Array.isArray(h)) {
    for (const [k, v] of h as [string, string][]) out[String(k).toLowerCase()] = String(v);
  } else {
    for (const [k, v] of Object.entries(h as Record<string, unknown>)) {
      out[k.toLowerCase()] = String(v);
    }
  }
  return out;
}

export type Responder = (req: CapturedRequest) => Response | Promise<Response>;

export function makeMockFetch(responder: Responder): {
  fetchImpl: (url: unknown, init?: Record<string, unknown>) => Promise<Response>;
  calls: CapturedRequest[];
} {
  const calls: CapturedRequest[] = [];
  const fetchImpl = async (url: unknown, init: Record<string, unknown> = {}): Promise<Response> => {
    const req: CapturedRequest = {
      url: String(url),
      method: init['method'] as string | undefined,
      headers: headersToObject(init['headers']),
      body: typeof init['body'] === 'string' ? (init['body'] as string) : undefined,
      signal: init['signal'] as AbortSignal | undefined,
    };
    calls.push(req);
    return await responder(req);
  };
  return { fetchImpl, calls };
}

export function jsonResponse(
  body: unknown,
  init: { status?: number; headers?: Record<string, string> } = {},
): Response {
  return new Response(JSON.stringify(body), {
    status: init.status ?? 200,
    headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
  });
}

/** Build a `text/event-stream` Response from raw SSE event strings. */
export function sseResponse(
  events: string[],
  init: { headers?: Record<string, string> } = {},
): Response {
  const enc = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const e of events) controller.enqueue(enc.encode(e));
      controller.close();
    },
  });
  return new Response(stream, {
    status: 200,
    headers: { 'content-type': 'text/event-stream', ...(init.headers ?? {}) },
  });
}

export const sseEvent = (obj: unknown): string => `data: ${JSON.stringify(obj)}\n\n`;
export const SSE_DONE = 'data: [DONE]\n\n';

// ---------------------------------------------------------------------------
// Canonical fixtures (OpenAI-shaped, per _protocol_invariants.py)
// ---------------------------------------------------------------------------

export function chatCompletionFixture(model = 'qwen-max'): Record<string, unknown> {
  return {
    id: 'chatcmpl-abc123',
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: 'Hello there.' },
        finish_reason: 'stop',
      },
    ],
    usage: { prompt_tokens: 9, completion_tokens: 3, total_tokens: 12 },
  };
}

export function chatChunkEvents(model = 'qwen-max', withUsage = false): string[] {
  const base = {
    id: 'chatcmpl-abc123',
    object: 'chat.completion.chunk',
    created: Math.floor(Date.now() / 1000),
    model,
  };
  const events = [
    sseEvent({ ...base, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }),
    sseEvent({ ...base, choices: [{ index: 0, delta: { content: 'Hi' }, finish_reason: null }] }),
    sseEvent({ ...base, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }),
  ];
  if (withUsage) {
    events.push(
      sseEvent({
        ...base,
        choices: [],
        usage: { prompt_tokens: 9, completion_tokens: 3, total_tokens: 12 },
      }),
    );
  }
  events.push(SSE_DONE);
  return events;
}

export function embeddingFixture(model = 'text-embedding-v1'): Record<string, unknown> {
  return {
    object: 'list',
    data: [{ object: 'embedding', index: 0, embedding: [0.01, -0.02, 0.03] }],
    model,
    usage: { prompt_tokens: 4, total_tokens: 4 },
  };
}

export function modelsListFixture(): Record<string, unknown> {
  return {
    object: 'list',
    data: [
      {
        id: 'qwen-max',
        object: 'model',
        created: 1,
        owned_by: 'he-api',
        capabilities: { chat: true, vision: false, embeddings: false },
      },
    ],
  };
}

/** He 5-field error envelope (mirror rest-api-spec.md §5.1.2). */
export function errorEnvelope(
  code: string,
  type = 'invalid_request_error',
  message = 'something went wrong',
): Record<string, unknown> {
  return { error: { message, type, code, param: null }, he_request_id: 'req_abc123def456' };
}

// ---------------------------------------------------------------------------
// Shape assertions (TS port of _protocol_invariants.py)
// ---------------------------------------------------------------------------

/* eslint-disable @typescript-eslint/no-explicit-any */
export function assertChatCompletionShape(r: any, expectedModel: string): void {
  expect(typeof r.id, 'id is string').toBe('string');
  expect(r.id, 'id chatcmpl- prefix').toMatch(CHATCMPL_ID_RE);
  expect(r.object).toBe('chat.completion');
  expect(typeof r.created).toBe('number');
  expect(r.created).toBeGreaterThan(0);
  expect(r.model).toBe(expectedModel);
  expect(Array.isArray(r.choices)).toBe(true);
  expect(r.choices.length).toBeGreaterThan(0);
  expect(r.choices[0].index).toBe(0);
  expect(r.choices[0].message.role).toBe('assistant');
  expect(typeof r.choices[0].message.content).toBe('string');
  expect(r.choices[0].message.content.length).toBeGreaterThan(0);
  expect(CANONICAL_FINISH_REASONS.has(r.choices[0].finish_reason)).toBe(true);
  expect(r.usage, 'usage populated').toBeTruthy();
  assertUsageTriple(r.usage);
}

export function assertChatChunkShape(c: any, expectedModel: string): void {
  expect(typeof c.id).toBe('string');
  expect(c.id).toMatch(CHATCMPL_ID_RE);
  expect(c.object).toBe('chat.completion.chunk');
  expect(c.model).toBe(expectedModel);
  expect(Array.isArray(c.choices)).toBe(true);
}

export function assertUsageTriple(u: any): void {
  expect(typeof u.prompt_tokens).toBe('number');
  expect(typeof u.completion_tokens).toBe('number');
  expect(typeof u.total_tokens).toBe('number');
  expect(u.prompt_tokens + u.completion_tokens, 'prompt+completion == total').toBe(u.total_tokens);
}

export function assertEmbeddingShape(r: any, expectedModel: string): void {
  expect(Array.isArray(r.data)).toBe(true);
  expect(r.data.length).toBeGreaterThan(0);
  expect(Array.isArray(r.data[0].embedding)).toBe(true);
  expect(r.data[0].embedding.length).toBeGreaterThan(0);
  expect(typeof r.data[0].embedding[0]).toBe('number');
  expect(r.model).toBe(expectedModel);
  expect(r.usage).toBeTruthy();
}

export function assertModelEntryShape(entry: any): void {
  expect(typeof entry.id).toBe('string');
  expect(entry.id.length).toBeGreaterThan(0);
  expect(entry.object).toBe('model');
  expect(entry.capabilities, 'capabilities present').toBeTruthy();
}
/* eslint-enable @typescript-eslint/no-explicit-any */
