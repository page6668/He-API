/**
 * Story 10.6 — browser → gateway client for the Playground proxy endpoint.
 *
 * The browser calls the JWT-cookie-authed gateway endpoint POST /v1/me/playground/chat
 * directly (OQ-10.6-1 (b) — NOT a console BFF). It NEVER holds a plaintext key;
 * the request body carries the owned `api_key_id`. The JWT cookie rides along via
 * `credentials: 'include'`. The base URL defaults to same-origin (the ingress
 * routes /v1/* to the gateway); override with NEXT_PUBLIC_HE_API_BASE.
 */
import { AB_MODELS_HEADER } from './ab';

export interface PlaygroundChatRequest {
  apiKeyId: string;
  model: string;
  system?: string;
  user: string;
  temperature?: number;
  maxTokens?: number;
  stream?: boolean;
  /** A/B header value "modelA,modelB" — when set, stream is forced off. */
  abModels?: string;
}

export function playgroundChatUrl(): string {
  const base = (process.env.NEXT_PUBLIC_HE_API_BASE ?? '').replace(/\/$/, '');
  return `${base}/v1/me/playground/chat`;
}

/** Build the JSON body the gateway expects (api_key_id + standard chat fields). */
export function buildRequestBody(req: PlaygroundChatRequest): Record<string, unknown> {
  const messages: Array<{ role: string; content: string }> = [];
  if (req.system && req.system.trim() !== '') messages.push({ role: 'system', content: req.system });
  messages.push({ role: 'user', content: req.user });
  const body: Record<string, unknown> = { api_key_id: req.apiKeyId, model: req.model, messages };
  if (typeof req.temperature === 'number') body.temperature = req.temperature;
  if (typeof req.maxTokens === 'number') body.max_tokens = req.maxTokens;
  // A/B is non-streaming only; never send stream:true with an A/B header.
  if (!req.abModels && req.stream) body.stream = true;
  return body;
}

/** POST a Playground chat request to the gateway. Returns the raw Response so the
 *  caller can stream the body (SSE) or parse JSON (non-stream / A/B). */
export function sendPlaygroundChat(req: PlaygroundChatRequest, signal?: AbortSignal): Promise<Response> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (req.abModels) headers[AB_MODELS_HEADER] = req.abModels;
  return fetch(playgroundChatUrl(), {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify(buildRequestBody(req)),
    signal,
  });
}
