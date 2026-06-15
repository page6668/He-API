# @he-api/sdk

Drop-in OpenAI-compatible **TypeScript SDK** for the [He-API](https://he-api.com) gateway.

`@he-api/sdk` is a thin subclass of the official [`openai`](https://www.npmjs.com/package/openai)
Node SDK (openai-node). It changes only the constructor defaults — the gateway
`baseURL` and the `HE_API_KEY` credential source — and inherits **everything
else**: chat / streaming / embeddings / models / audio / vision, SSE iteration,
retries, timeouts, and the complete TypeScript type surface.

## Install

```sh
npm install @he-api/sdk
# or: pnpm add @he-api/sdk
```

## Quickstart

Migrating from the official SDK is a one-line import swap:

```ts
// before:
// import OpenAI from 'openai';
// const client = new OpenAI();

import { Client } from '@he-api/sdk';

const client = new Client(); // baseURL → He gateway, credential ← HE_API_KEY

const res = await client.chat.completions.create({
  model: 'qwen-max',
  messages: [{ role: 'user', content: 'Hi' }],
});
console.log(res.choices[0]?.message.content);
```

### Credentials & base URL

- Credential is read from **`HE_API_KEY`** (or the `apiKey` constructor option).
  `OPENAI_API_KEY` is **never** read — this prevents a real OpenAI key from
  being cross-wired to the He gateway.
- `baseURL` defaults to `https://api.he-api.com/v1`. Override precedence:
  constructor `baseURL` > `HE_API_BASE_URL` env > default.

### Streaming

```ts
const stream = await client.chat.completions.create({
  model: 'qwen-max',
  messages: [{ role: 'user', content: 'Hi' }],
  stream: true,
  stream_options: { include_usage: true },
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? '');
}
```

### He extensions

Per-request He headers (routing strategy, A/B) pass straight through; the SDK
does not re-validate them (the gateway does):

```ts
await client.chat.completions.create(
  { model: 'he-router-cost', messages: [{ role: 'user', content: 'Hi' }] },
  { headers: { 'X-He-Routing-Strategy': 'cost' } },
);
```

Read He response headers via the inherited `.withResponse()`:

```ts
const { data, response } = await client.chat.completions
  .create({ model: 'qwen-max', messages: [{ role: 'user', content: 'Hi' }] })
  .withResponse();
const selected = response.headers.get('x-he-selected-model');
// Note: X-He-Cost-Usd is non-authoritative and may be absent — never bill from it.
```

He-only endpoints:

```ts
await client.balance({ currency: 'usd' }); // GET /v1/balance?currency=usd
await client.usage({ currency: 'usd' });   // GET /v1/usage
```

Errors surface as openai-node's exception hierarchy, with He custom codes
readable on `.code`:

```ts
import { BadRequestError } from '@he-api/sdk';
try {
  await client.chat.completions.create({ model: 'nope', messages: [] });
} catch (err) {
  if (err instanceof BadRequestError) {
    console.error(err.code); // e.g. "400_invalid_request"
  }
}
```

Audio (`client.audio.*`) and multimodal/vision messages are auto-inherited from
openai-node and need no extra code.

## Module format — ESM only

> **This package is published as pure ESM** (`"type": "module"`, `exports`
> exposes `import` + `types` only — there is **no `require` condition**). It
> must be consumed from an ES module (`import`), modern Node (≥ 18), or a
> bundler. Legacy CommonJS `require('@he-api/sdk')` is **not** supported in this
> release. A dual ESM + CJS build is a deliberate **fast-follow** if CJS demand
> surfaces — it is intentionally out of scope here (see story 10.3 R-OQ-10.3-3 /
> LOW-1), not an oversight.

## Requirements

- **Node ≥ 18** (`engines.node`). This is intentionally looser than the He-API
  console's `>=20.11`: this is an externally consumed SDK and the pure-passthrough
  client uses no Node-20-only API, so the floor tracks our `openai` dependency to
  maximize drop-in reach.
- Node-first. Browser usage is **not** supported in this release (the client does
  not set `dangerouslyAllowBrowser`).

## License

MIT
