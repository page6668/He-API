/**
 * `@he-api/sdk` — drop-in OpenAI-compatible TypeScript client for the He-API
 * gateway.
 *
 * ESM-only. `Client extends OpenAI` (openai-node), so everything the official
 * `openai` package offers is inherited; only the constructor defaults differ.
 *
 * @example
 * ```ts
 * import { Client } from '@he-api/sdk';
 * const client = new Client(); // baseURL → He gateway, key ← HE_API_KEY
 * const res = await client.chat.completions.create({
 *   model: 'qwen-max',
 *   messages: [{ role: 'user', content: 'Hi' }],
 * });
 * ```
 */

// Re-export the full openai-node surface — request/response types AND error
// classes (APIError, BadRequestError, AuthenticationError, RateLimitError,
// APIConnectionError, OpenAIError, …) — so code written against `openai`
// (type annotations, `instanceof OpenAI.BadRequestError`) keeps working after
// a one-line import swap (BR-10.3.8 / R-OQ-10.3-4).
export * from 'openai';
export { default as OpenAI } from 'openai';

export { Client, DEFAULT_BASE_URL } from './client.js';
export type { HeClientOptions } from './client.js';
