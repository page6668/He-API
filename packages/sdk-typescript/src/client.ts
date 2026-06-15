import OpenAI, { OpenAIError, type ClientOptions } from 'openai';

/**
 * Default He-API gateway base URL.
 *
 * The He-API gateway speaks the OpenAI wire protocol (see
 * `docs/architecture/rest-api-spec.md §5.1`), so the inherited openai-node
 * surfaces work unchanged once requests are pointed here. A single const keeps
 * the default in one place (R-OQ-10.3-3).
 */
export const DEFAULT_BASE_URL = 'https://api.he-api.com/v1';

/**
 * Construction options for the He-API {@link Client}.
 *
 * Identical to openai-node's `ClientOptions` — the He client only changes the
 * defaults for `baseURL` (→ He gateway) and the credential source (→
 * `HE_API_KEY`). Every other option is forwarded verbatim to openai-node.
 */
export type HeClientOptions = ClientOptions;

/**
 * Drop-in He-API client.
 *
 * `Client extends OpenAI` (openai-node subclass, R-OQ-10.3-1): chat /
 * streaming / embeddings / models / audio / vision, SSE iteration, retries,
 * timeouts and the full TypeScript type surface are **inherited** from
 * openai-node, never reimplemented. The subclass overrides only the
 * constructor to inject two He defaults:
 *
 *   1. `baseURL` → the He-API gateway (overridable via ctor arg or
 *      `HE_API_BASE_URL`), and
 *   2. the credential is read from `HE_API_KEY` (never `OPENAI_API_KEY`), so a
 *      real-OpenAI key in the environment can never be cross-wired to the He
 *      gateway.
 *
 * Migration is a one-line import swap:
 * ```ts
 * // before:  import OpenAI from 'openai';        const client = new OpenAI();
 * // after:   import { Client } from '@he-api/sdk'; const client = new Client();
 * ```
 */
export class Client extends OpenAI {
  constructor(options: HeClientOptions = {}) {
    // baseURL precedence: explicit ctor arg > HE_API_BASE_URL env > He default.
    // (Nullish coalescing — an explicitly-passed empty string is the caller's
    // problem, matching openai-node's own permissiveness.)
    const baseURL = options.baseURL ?? process.env['HE_API_BASE_URL'] ?? DEFAULT_BASE_URL;

    // Credential precedence: explicit apiKey > HE_API_KEY env. We resolve the
    // key ourselves so openai-node's constructor default (which would read
    // OPENAI_API_KEY) is never reached. An empty string is treated as missing
    // so we never emit an `Authorization: Bearer ` with an empty credential.
    const rawKey = options.apiKey ?? process.env['HE_API_KEY'];
    const apiKey = typeof rawKey === 'string' && rawKey.length > 0 ? rawKey : undefined;
    if (apiKey === undefined) {
      throw new OpenAIError(
        'The HE_API_KEY environment variable is missing or empty; either provide it, ' +
          "or instantiate the He-API client with an apiKey option, like new Client({ apiKey: 'My API Key' }).",
      );
    }

    super({ ...options, apiKey, baseURL });
  }

  /**
   * Convenience for the He-only `GET /v1/balance` endpoint (not part of the
   * OpenAI surface). Thin wrapper over the inherited `client.get` escape hatch
   * (R-OQ-10.3-4c). The gateway validates `currency`; the SDK does not
   * re-validate it.
   */
  balance(params?: { currency?: string }): Promise<unknown> {
    return this.get('/balance', { query: params });
  }

  /**
   * Convenience for the He-only `GET /v1/usage` endpoint. Thin wrapper over the
   * inherited `client.get` escape hatch (R-OQ-10.3-4c).
   */
  usage(params?: Record<string, string | number | undefined>): Promise<unknown> {
    return this.get('/usage', { query: params });
  }
}
