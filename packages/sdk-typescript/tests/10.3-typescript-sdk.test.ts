/**
 * Tests for Story 10.3: TypeScript SDK (`@he-api/sdk`, drop-in OpenAI)
 *
 * Implemented from the QA Test Design skeleton (Turing, 2026-06-15).
 * Test Design: docs/qa/assessments/10.3-test-design-20260615.md
 *
 * Conventions (ratified — see story "Ratified Decisions"):
 *   - `Client extends OpenAI` (openai-node subclass, R-OQ-1) — only the constructor is overridden.
 *   - Hermetic: inject a mock `fetch` into the openai-node client (R-OQ-5) — NO real gateway.
 *   - Drop-in oracle: fixtures shaped per apps/api-gateway/tests/_protocol_invariants.py.
 *
 * Note on openai-node version (Dev): npm `latest` is 6.x, but per ratified
 * R-OQ-10.3-1 (caret pin to the *validated* major+floor, band `>=4.x,<5`) and the
 * Python drop-in oracle (`openai==1.40.*`-era shapes), this SDK pins `openai@^4`.
 * The pin-drift guard (10.3-UNIT-003) enforces that band and fails loud outside it.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, test } from 'vitest';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  Client,
  DEFAULT_BASE_URL,
  OpenAI,
  APIError,
  APIConnectionError,
  BadRequestError,
} from '../src/index.js';
import {
  assertChatChunkShape,
  assertChatCompletionShape,
  assertEmbeddingShape,
  assertModelEntryShape,
  assertUsageTriple,
  chatChunkEvents,
  chatCompletionFixture,
  embeddingFixture,
  errorEnvelope,
  jsonResponse,
  makeMockFetch,
  modelsListFixture,
  REQUEST_ID_RE,
  sseResponse,
  type CapturedRequest,
  type Responder,
} from './_helpers.js';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PKG_ROOT = join(__dirname, '..');
const require = createRequire(import.meta.url);

/** Build a Client whose transport is a captured mock fetch. */
function clientWith(
  responder: Responder,
  options: Record<string, unknown> = {},
): { client: Client; calls: CapturedRequest[] } {
  const { fetchImpl, calls } = makeMockFetch(responder);
  const client = new Client({
    apiKey: 'sk-test',
    maxRetries: 0,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    fetch: fetchImpl as any,
    ...options,
  });
  return { client, calls };
}

const chatResponder = (model = 'qwen-max', headers?: Record<string, string>): Responder => {
  return () => jsonResponse(chatCompletionFixture(model), headers ? { headers } : {});
};

// ============================================================
// AC1: `npm install @he-api/sdk` 可用 — publishable, typed npm package
// ============================================================

describe('AC1: publishable, typed npm package', () => {
  const readPkg = (): Record<string, any> =>
    JSON.parse(readFileSync(join(PKG_ROOT, 'package.json'), 'utf8'));

  // --- P0 ---

  test('10.3-UNIT-001: import { Client } resolves and is a constructor', () => {
    expect(typeof Client).toBe('function');
    const c = new Client({ apiKey: 'sk-test' });
    expect(c).toBeInstanceOf(Client);
    expect(c).toBeInstanceOf(OpenAI); // subclass (R-OQ-1)
  });

  test('10.3-UNIT-002: package.json is publishable (non-private + publishConfig.access=public + real version)', () => {
    const pkg = readPkg();
    expect(pkg.private).not.toBe(true);
    expect(pkg.publishConfig?.access).toBe('public');
    expect(pkg.version).not.toBe('0.0.0');
    expect(pkg.version).toMatch(/^\d+\.\d+\.\d+/);
    expect(pkg.name).toBe('@he-api/sdk');
  });

  test('10.3-UNIT-003: pin-drift guard — installed openai-node within validated caret band, fail-loud outside', () => {
    // Validated band: >=4.104.0 <5.0.0 (R-OQ-10.3-1; npm latest is 6.x but this
    // SDK pins ^4 for drop-in-oracle parity). Fail loudly outside the band so an
    // upstream major bump that changes parse shapes is caught early.
    const openaiPkg = JSON.parse(
      readFileSync(join(dirname(require.resolve('openai')), 'package.json'), 'utf8'),
    ) as { version: string };
    const [major, minor] = openaiPkg.version.split('.').map((n) => Number.parseInt(n, 10));
    const FLOOR_MINOR = 104;
    const inBand = major === 4 && minor >= FLOOR_MINOR;
    expect(
      inBand,
      `openai-node ${openaiPkg.version} is OUTSIDE the validated band >=4.${FLOOR_MINOR}.0 <5.0.0 — ` +
        `re-validate drop-in shapes before widening the pin (BR-10.3.9, R-OQ-10.3-1).`,
    ).toBe(true);

    // The declared dependency range must also stay on the validated major.
    const declared = readPkg().dependencies?.openai as string;
    expect(declared).toMatch(/^\^4\./);
  });

  test('10.3-INT-001: tsc build emits dist/ + .d.ts, exports.types points to declaration', () => {
    ensureBuilt();
    const dts = join(PKG_ROOT, 'dist', 'index.d.ts');
    expect(existsSync(dts)).toBe(true);
    expect(existsSync(join(PKG_ROOT, 'dist', 'index.js'))).toBe(true);
    const pkg = readPkg();
    const typesPath = pkg.exports?.['.']?.types ?? pkg.types;
    expect(typesPath).toBeTruthy();
    expect(existsSync(join(PKG_ROOT, typesPath))).toBe(true);
  });

  test('10.3-INT-002: npm pack tgz contains only dist (no src, no tests)', () => {
    ensureBuilt();
    const out = execFileSync('npm', ['pack', '--dry-run', '--json'], {
      cwd: PKG_ROOT,
      encoding: 'utf8',
    });
    const parsed = JSON.parse(out) as Array<{ files: Array<{ path: string }> }>;
    const paths = parsed[0]?.files.map((f) => f.path) ?? [];
    expect(paths.length).toBeGreaterThan(0);
    // npm always includes package.json + README; the hygiene rule is: NO src/, NO tests/.
    expect(paths.some((p) => p.startsWith('src/'))).toBe(false);
    expect(paths.some((p) => p.startsWith('tests/'))).toBe(false);
    expect(paths.some((p) => p.startsWith('dist/'))).toBe(true);
    expect(paths).toContain('dist/index.d.ts');
  });

  test('10.3-INT-003: consumer-side tsc against built .d.ts resolves Client with no `any` degradation', () => {
    ensureBuilt();
    const tmp = mkdtempSync(join(tmpdir(), 'he-sdk-consumer-'));
    try {
      // If Client were `any`, IsAny<…> resolves to `true` ⇒ the annotation type
      // becomes `never` ⇒ assigning `true` fails to compile. tsc success ⇒ not-any.
      const consumer = join(tmp, 'consumer.ts');
      const distIndex = join(PKG_ROOT, 'dist', 'index.js');
      writeFileSync(
        consumer,
        [
          `import { Client } from ${JSON.stringify(distIndex)};`,
          `type IsAny<T> = 0 extends (1 & T) ? true : false;`,
          `const _notAny: IsAny<InstanceType<typeof Client>> extends true ? never : true = true;`,
          `void _notAny;`,
          `const c = new Client({ apiKey: 'x' });`,
          `// Real method signature must be present (would be missing if Client were any-shaped).`,
          `const _p: Promise<unknown> = c.chat.completions.create({ model: 'm', messages: [{ role: 'user', content: 'hi' }] });`,
          `void _p;`,
        ].join('\n'),
        'utf8',
      );
      const tsc = require.resolve('typescript/bin/tsc');
      // Throws (non-zero exit) if the consumer fails to type-check.
      execFileSync(
        process.execPath,
        [
          tsc,
          '--noEmit',
          '--strict',
          '--skipLibCheck',
          '--module',
          'esnext',
          '--moduleResolution',
          'bundler',
          '--target',
          'es2022',
          consumer,
        ],
        { cwd: PKG_ROOT, encoding: 'utf8' },
      );
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  // --- P1 ---

  test('10.3-UNIT-004: exports map = types + import only, NO require condition (pure ESM)', () => {
    const pkg = readPkg();
    const dot = pkg.exports?.['.'];
    expect(dot).toBeTruthy();
    expect(dot.types).toBeTruthy();
    expect(dot.import).toBeTruthy();
    expect(dot.require, 'pure ESM ⇒ no require condition (R-OQ-3)').toBeUndefined();
    expect(pkg.type).toBe('module');
  });

  test('10.3-UNIT-005: engines.node === ">=18" (not reflexively bumped to >=20.11)', () => {
    expect(readPkg().engines?.node).toBe('>=18');
  });

  test('10.3-UNIT-006: metadata completeness (name, description, license, repository+homepage URLs)', () => {
    const pkg = readPkg();
    expect(pkg.name).toBe('@he-api/sdk');
    expect(typeof pkg.description).toBe('string');
    expect(pkg.description.length).toBeGreaterThan(0);
    expect(typeof pkg.license).toBe('string');
    expect(pkg.repository?.url ?? pkg.repository).toBeTruthy();
    expect(typeof pkg.homepage).toBe('string');
    expect(pkg.homepage).toMatch(/^https?:\/\//);
  });

  // --- P2 (doc gate) ---

  test('10.3-DOC-001: README states ESM-only requirement + deferred-dual-format note', () => {
    const readme = readFileSync(join(PKG_ROOT, 'README.md'), 'utf8');
    expect(readme).toMatch(/ESM[- ]only/i);
    expect(readme.toLowerCase()).toMatch(/dual|cjs|commonjs|require/);
    expect(readme.toLowerCase()).toMatch(/fast-follow|deferred|out of scope/);
  });
});

// ============================================================
// AC2: 与 OpenAI Node/TS SDK 接口一致 — drop-in interface identity
// ============================================================

describe('AC2: drop-in interface identity', () => {
  // Hermetic env isolation: these globals must never leak between tests.
  const ENV_KEYS = ['HE_API_KEY', 'HE_API_BASE_URL', 'OPENAI_API_KEY', 'OPENAI_BASE_URL'];
  let savedEnv: Record<string, string | undefined> = {};
  beforeEach(() => {
    savedEnv = {};
    for (const k of ENV_KEYS) {
      savedEnv[k] = process.env[k];
      delete process.env[k];
    }
  });
  afterEach(() => {
    for (const k of ENV_KEYS) {
      if (savedEnv[k] === undefined) delete process.env[k];
      else process.env[k] = savedEnv[k];
    }
  });

  // --- P0: resolution (unit) ---

  test('10.3-UNIT-010: no baseURL passed ⇒ targets DEFAULT_BASE_URL https://api.he-api.com/v1', async () => {
    const { client, calls } = clientWith(chatResponder());
    expect(client.baseURL).toBe(DEFAULT_BASE_URL);
    expect(DEFAULT_BASE_URL).toBe('https://api.he-api.com/v1');
    await client.chat.completions.create({ model: 'qwen-max', messages: [{ role: 'user', content: 'hi' }] });
    expect(calls[0]?.url.startsWith('https://api.he-api.com/v1')).toBe(true);
  });

  test('10.3-UNIT-011: baseURL precedence ctor > HE_API_BASE_URL > default', () => {
    // default
    expect(new Client({ apiKey: 'k' }).baseURL).toBe(DEFAULT_BASE_URL);
    // env over default
    process.env['HE_API_BASE_URL'] = 'https://env.example.com/v1';
    expect(new Client({ apiKey: 'k' }).baseURL).toBe('https://env.example.com/v1');
    // ctor over env
    expect(new Client({ apiKey: 'k', baseURL: 'https://ctor.example.com/v1' }).baseURL).toBe(
      'https://ctor.example.com/v1',
    );
  });

  test('10.3-UNIT-012: apiKey explicit > HE_API_KEY; OPENAI_API_KEY is NOT read', () => {
    // explicit wins
    expect(() => new Client({ apiKey: 'explicit' })).not.toThrow();
    // HE_API_KEY used when no explicit key
    process.env['HE_API_KEY'] = 'from-he-env';
    expect(() => new Client()).not.toThrow();
    expect(new Client().apiKey).toBe('from-he-env');
    // OPENAI_API_KEY must NOT be picked up
    delete process.env['HE_API_KEY'];
    process.env['OPENAI_API_KEY'] = 'sk-real-openai';
    expect(() => new Client()).toThrow(/HE_API_KEY/);
  });

  // --- P0: drop-in core (integration, mock fetch) ---

  test('10.3-INT-010: chat.completions.create non-stream ⇒ OpenAI-shaped ChatCompletion, hits gateway baseURL', async () => {
    const { client, calls } = clientWith(chatResponder('qwen-max'));
    const res = await client.chat.completions.create({
      model: 'qwen-max',
      messages: [{ role: 'user', content: 'hi' }],
    });
    assertChatCompletionShape(res, 'qwen-max');
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/chat/completions');
  });

  test('10.3-INT-011: chat stream:true ⇒ for await yields chunk-shaped objects, terminates on [DONE]', async () => {
    const { client } = clientWith(() => sseResponse(chatChunkEvents('qwen-max')));
    const stream = await client.chat.completions.create({
      model: 'qwen-max',
      messages: [{ role: 'user', content: 'hi' }],
      stream: true,
    });
    const chunks: unknown[] = [];
    for await (const chunk of stream) {
      assertChatChunkShape(chunk, 'qwen-max');
      chunks.push(chunk);
    }
    expect(chunks.length).toBeGreaterThan(0); // iteration terminated cleanly on [DONE]
  });

  test('10.3-INT-012: streaming include_usage tail chunk ⇒ usage present, prompt+completion=total invariant', async () => {
    const { client } = clientWith(() => sseResponse(chatChunkEvents('qwen-max', true)));
    const stream = await client.chat.completions.create({
      model: 'qwen-max',
      messages: [{ role: 'user', content: 'hi' }],
      stream: true,
      stream_options: { include_usage: true },
    });
    let usage: { prompt_tokens: number; completion_tokens: number; total_tokens: number } | undefined;
    for await (const chunk of stream) {
      if (chunk.usage) usage = chunk.usage as typeof usage;
    }
    expect(usage, 'tail chunk carries usage').toBeTruthy();
    assertUsageTriple(usage);
  });

  test('10.3-INT-013: embeddings.create ⇒ embedding-shaped object (data[].embedding, model, usage)', async () => {
    const { client } = clientWith(() => jsonResponse(embeddingFixture('text-embedding-v1')));
    // encoding_format:'float' so the JSON number[] passes through as-is (openai-node
    // otherwise auto-requests base64 and decodes it — orthogonal to the shape check).
    const res = await client.embeddings.create({
      model: 'text-embedding-v1',
      input: 'hello',
      encoding_format: 'float',
    });
    assertEmbeddingShape(res, 'text-embedding-v1');
  });

  test('10.3-INT-014: models.list ⇒ iterable of model entries incl. capabilities', async () => {
    const { client, calls } = clientWith(() => jsonResponse(modelsListFixture()));
    const page = await client.models.list();
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/models');
    expect(page.data.length).toBeGreaterThan(0);
    assertModelEntryShape(page.data[0]);
  });

  test('10.3-INT-015: 400 He-envelope ⇒ BadRequestError, .code=="400_invalid_request", he_request_id readable', async () => {
    const { client } = clientWith(() =>
      jsonResponse(errorEnvelope('400_invalid_request'), {
        status: 400,
        headers: { 'x-he-request-id': 'req_abc123def456', 'x-request-id': 'req_abc123def456' },
      }),
    );
    try {
      await client.chat.completions.create({ model: 'nope', messages: [{ role: 'user', content: 'x' }] });
      expect.unreachable('should have thrown BadRequestError');
    } catch (err) {
      expect(err).toBeInstanceOf(BadRequestError);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const e = err as any;
      expect(e.code).toBe('400_invalid_request');
      // he_request_id is readable from the raw header (and openai maps x-request-id → .request_id)
      const heReqId = e.headers?.['x-he-request-id'] ?? e.request_id;
      expect(heReqId).toMatch(REQUEST_ID_RE);
    }
  });

  test('10.3-INT-016: 402 ⇒ OpenAI.APIError subclass, .code=="402_quota_exhausted" not swallowed', async () => {
    const { client } = clientWith(() =>
      jsonResponse(errorEnvelope('402_quota_exhausted', 'invalid_request_error', 'quota exhausted'), {
        status: 402,
      }),
    );
    try {
      await client.chat.completions.create({ model: 'qwen-max', messages: [{ role: 'user', content: 'x' }] });
      expect.unreachable('should have thrown APIError');
    } catch (err) {
      expect(err).toBeInstanceOf(APIError);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      expect((err as any).code).toBe('402_quota_exhausted');
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      expect((err as any).status).toBe(402);
    }
  });

  // --- P1: He extensions ---

  test('10.3-UNIT-013: openai-node types + error classes re-exported; instanceof holds', async () => {
    // Re-exported classes are the very same as openai-node's (identity), so
    // `instanceof` on a caught error holds (BR-10.3.8).
    expect(BadRequestError).toBe(OpenAI.BadRequestError);
    expect(APIError).toBe(OpenAI.APIError);
    const { client } = clientWith(() => jsonResponse(errorEnvelope('400_invalid_request'), { status: 400 }));
    try {
      await client.chat.completions.create({ model: 'x', messages: [{ role: 'user', content: 'x' }] });
      expect.unreachable();
    } catch (err) {
      expect(err instanceof BadRequestError).toBe(true);
      expect(err instanceof APIError).toBe(true);
    }
  });

  test('10.3-INT-020: per-request X-He-Routing-Strategy emitted on wire; SDK does NOT re-validate', async () => {
    const { client, calls } = clientWith(chatResponder());
    // An arbitrary (even nonsensical) value forwards unchanged — validation is the gateway's job.
    await client.chat.completions.create(
      { model: 'he-router-cost', messages: [{ role: 'user', content: 'hi' }] },
      { headers: { 'X-He-Routing-Strategy': 'cost' } },
    );
    expect(calls[0]?.headers['x-he-routing-strategy']).toBe('cost');
  });

  test('10.3-INT-021: per-request X-He-AB-Models passes through unchanged on wire', async () => {
    const { client, calls } = clientWith(chatResponder());
    await client.chat.completions.create(
      { model: 'qwen-max', messages: [{ role: 'user', content: 'hi' }] },
      { headers: { 'X-He-AB-Models': 'qwen-max,glm-4' } },
    );
    expect(calls[0]?.headers['x-he-ab-models']).toBe('qwen-max,glm-4');
  });

  test('10.3-INT-022: .withResponse() exposes raw X-He-Request-Id / X-He-Selected-Model', async () => {
    const { client } = clientWith(
      chatResponder('qwen-max', {
        'x-he-request-id': 'req_abc123def456',
        'x-he-selected-model': 'qwen-max',
      }),
    );
    const { data, response } = await client.chat.completions
      .create({ model: 'qwen-max', messages: [{ role: 'user', content: 'hi' }] })
      .withResponse();
    assertChatCompletionShape(data, 'qwen-max');
    expect(response.headers.get('x-he-request-id')).toBe('req_abc123def456');
    expect(response.headers.get('x-he-selected-model')).toBe('qwen-max');
  });

  test('10.3-INT-023: X-He-Cost-Usd absence-tolerant (OQ5) — no assumption of presence, no billing from it', async () => {
    // Gateway intentionally omits X-He-Cost-Usd (OQ5). Reading it returns null;
    // the SDK never fabricates or bills from a cost value.
    const { client } = clientWith(chatResponder('qwen-max', { 'x-he-request-id': 'req_abc123def456' }));
    const { data, response } = await client.chat.completions
      .create({ model: 'qwen-max', messages: [{ role: 'user', content: 'hi' }] })
      .withResponse();
    expect(response.headers.get('x-he-cost-usd')).toBeNull();
    expect(data).not.toHaveProperty('cost_usd');
  });

  test('10.3-INT-024: /v1/balance convenience — path + ?currency=usd correct on wire, parsed JSON returned', async () => {
    const { client, calls } = clientWith(() => jsonResponse({ balance: 12.34, currency: 'usd' }));
    const res = (await client.balance({ currency: 'usd' })) as { balance: number; currency: string };
    expect(calls[0]?.method).toBe('GET');
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/balance?currency=usd');
    expect(res.balance).toBe(12.34);
  });

  test('10.3-INT-025: /v1/usage convenience — path + query correct on wire', async () => {
    const { client, calls } = clientWith(() => jsonResponse({ usage: [] }));
    await client.usage({ currency: 'usd' });
    expect(calls[0]?.method).toBe('GET');
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/usage?currency=usd');
  });

  test.skipIf(!process.env['HE_API_TEST_GATEWAY_URL'])(
    '10.3-E2E-001: [LIVE, gated on HE_API_TEST_GATEWAY_URL] new Client() drop-in vs real gateway',
    async () => {
      const client = new Client({ baseURL: process.env['HE_API_TEST_GATEWAY_URL'] });
      const res = await client.chat.completions.create({
        model: process.env['HE_API_TEST_MODEL'] ?? 'qwen-max',
        messages: [{ role: 'user', content: 'ping' }],
      });
      assertChatCompletionShape(res, res.model);
    },
  );

  // --- P2: auto-inherited surfaces (docs + one smoke each, NO new impl) ---

  test('10.3-UNIT-014: default client does NOT enable dangerouslyAllowBrowser', () => {
    const client = new Client({ apiKey: 'sk-test' });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect((client as any)._options?.dangerouslyAllowBrowser).not.toBe(true);
  });

  test('10.3-INT-030: client.audio.transcriptions.create reachable via inheritance (mock smoke)', async () => {
    const { client, calls } = clientWith(() => jsonResponse({ text: 'hello world' }));
    const file = new File([new Uint8Array([0x52, 0x49, 0x46, 0x46])], 'audio.wav', { type: 'audio/wav' });
    const res = (await client.audio.transcriptions.create({ file, model: 'whisper-1' })) as {
      text: string;
    };
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/audio/transcriptions');
    expect(res.text).toBe('hello world');
  });

  test('10.3-INT-031: client.audio.speech.create reachable (mock smoke, binary)', async () => {
    const { client, calls } = clientWith(
      () =>
        new Response(new Uint8Array([0x49, 0x44, 0x33]), {
          status: 200,
          headers: { 'content-type': 'audio/mpeg' },
        }),
    );
    const res = await client.audio.speech.create({ model: 'tts-1', voice: 'alloy', input: 'hi' });
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/audio/speech');
    const buf = await res.arrayBuffer();
    expect(buf.byteLength).toBeGreaterThan(0);
  });

  test('10.3-INT-032: multimodal chat with image content-part passes through (mock smoke)', async () => {
    const { client, calls } = clientWith(chatResponder('qwen-vl-max'));
    await client.chat.completions.create({
      model: 'qwen-vl-max',
      messages: [
        {
          role: 'user',
          content: [
            { type: 'text', text: 'what is this?' },
            { type: 'image_url', image_url: { url: 'https://example.com/cat.png' } },
          ],
        },
      ],
    });
    expect(calls[0]?.body).toContain('image_url');
    expect(calls[0]?.body).toContain('https://example.com/cat.png');
  });
});

// ============================================================
// Blind Spot Scenarios [BLIND-SPOT] (library: BOUNDARY, ERROR, RESOURCE; CONCURRENCY)
// ============================================================

describe('[BLIND-SPOT] boundary / error / concurrency / resource', () => {
  const ENV_KEYS = ['HE_API_KEY', 'HE_API_BASE_URL', 'OPENAI_API_KEY', 'OPENAI_BASE_URL'];
  let savedEnv: Record<string, string | undefined> = {};
  beforeEach(() => {
    savedEnv = {};
    for (const k of ENV_KEYS) {
      savedEnv[k] = process.env[k];
      delete process.env[k];
    }
  });
  afterEach(() => {
    for (const k of ENV_KEYS) {
      if (savedEnv[k] === undefined) delete process.env[k];
      else process.env[k] = savedEnv[k];
    }
  });

  test('[BLIND-SPOT] 10.3-BLIND-BOUNDARY-001: no apiKey + no HE_API_KEY ⇒ native missing-key error', () => {
    expect(() => new Client()).toThrow(/HE_API_KEY/);
    // It must be openai-node's own error class, not a generic Error.
    try {
      new Client();
    } catch (err) {
      expect(err).toBeInstanceOf(OpenAI.OpenAIError);
    }
  });

  test('[BLIND-SPOT] 10.3-BLIND-BOUNDARY-002: empty-string apiKey / HE_API_KEY treated as missing', () => {
    expect(() => new Client({ apiKey: '' })).toThrow(/HE_API_KEY/);
    process.env['HE_API_KEY'] = '';
    expect(() => new Client()).toThrow(/HE_API_KEY/);
  });

  test('[BLIND-SPOT] 10.3-BLIND-BOUNDARY-003: baseURL path-join edges (trailing slash / missing /v1)', async () => {
    // trailing slash must not produce a double slash on the wire
    const { client, calls } = clientWith(() => jsonResponse({ balance: 1, currency: 'usd' }), {
      baseURL: 'https://api.he-api.com/v1/',
    });
    await client.balance({ currency: 'usd' });
    expect(calls[0]?.url).toBe('https://api.he-api.com/v1/balance?currency=usd');
    expect(calls[0]?.url).not.toMatch(/\/v1\/\/+/);
  });

  test('[BLIND-SPOT] 10.3-BLIND-ERROR-001: connection refused ⇒ APIConnectionError propagates', async () => {
    const { client } = clientWith(() => {
      throw new TypeError('fetch failed: connection refused');
    });
    await expect(
      client.chat.completions.create({ model: 'qwen-max', messages: [{ role: 'user', content: 'x' }] }),
    ).rejects.toBeInstanceOf(APIConnectionError);
  });

  test('[BLIND-SPOT] 10.3-BLIND-ERROR-002: malformed SSE / invalid JSON mid-stream ⇒ surfaced, no hang', async () => {
    const { client } = clientWith(() =>
      sseResponse(['data: {not valid json\n\n', 'data: [DONE]\n\n']),
    );
    const stream = await client.chat.completions.create({
      model: 'qwen-max',
      messages: [{ role: 'user', content: 'x' }],
      stream: true,
    });
    await expect(
      (async () => {
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        for await (const _chunk of stream) {
          /* drain */
        }
      })(),
    ).rejects.toThrow();
  });

  test('[BLIND-SPOT] 10.3-BLIND-ERROR-003: upstream 5xx/504 ⇒ APIError subclass with status', async () => {
    const { client } = clientWith(() =>
      jsonResponse(errorEnvelope('503_upstream', 'server_error', 'upstream unavailable'), { status: 503 }),
    );
    try {
      await client.chat.completions.create({ model: 'qwen-max', messages: [{ role: 'user', content: 'x' }] });
      expect.unreachable('should have thrown');
    } catch (err) {
      expect(err).toBeInstanceOf(APIError);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      expect((err as any).status).toBe(503);
    }
  });

  test('[BLIND-SPOT] 10.3-BLIND-CONCURRENCY-001: concurrent create calls ⇒ per-request headers do not bleed', async () => {
    const { client, calls } = clientWith(chatResponder());
    await Promise.all([
      client.chat.completions.create(
        { model: 'qwen-max', messages: [{ role: 'user', content: 'a' }] },
        { headers: { 'X-He-Routing-Strategy': 'cost' } },
      ),
      client.chat.completions.create(
        { model: 'qwen-max', messages: [{ role: 'user', content: 'b' }] },
        { headers: { 'X-He-Routing-Strategy': 'latency' } },
      ),
    ]);
    const strategies = calls.map((c) => c.headers['x-he-routing-strategy']).sort();
    expect(strategies).toEqual(['cost', 'latency']);
    // No request carries both — headers stayed isolated per call.
    expect(calls.every((c) => c.headers['x-he-routing-strategy'] !== undefined)).toBe(true);
  });

  test('[BLIND-SPOT] 10.3-BLIND-RESOURCE-001: break out of for-await stream ⇒ connection aborted/released', async () => {
    const { client, calls } = clientWith(() => sseResponse(chatChunkEvents('qwen-max')));
    const stream = await client.chat.completions.create({
      model: 'qwen-max',
      messages: [{ role: 'user', content: 'x' }],
      stream: true,
    });
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    for await (const _chunk of stream) {
      break; // early exit ⇒ openai-node aborts the underlying controller
    }
    await new Promise((r) => setTimeout(r, 20));
    expect(calls[0]?.signal?.aborted).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Build-once helper for the artifact tests (turbo `test` does not self-build).
// ---------------------------------------------------------------------------
let built = false;
function ensureBuilt(): void {
  if (built && existsSync(join(PKG_ROOT, 'dist', 'index.d.ts'))) return;
  const tsc = require.resolve('typescript/bin/tsc');
  execFileSync(process.execPath, [tsc, '-p', join(PKG_ROOT, 'tsconfig.json')], {
    cwd: PKG_ROOT,
    encoding: 'utf8',
  });
  built = true;
}

beforeAll(() => {
  // Warm the build once so artifact tests are order-independent.
  ensureBuilt();
});
