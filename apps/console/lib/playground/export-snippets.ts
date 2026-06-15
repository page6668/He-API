/**
 * Story 10.6 — Export-as code-snippet generators (FR-11.2, BR-10.6.4).
 *
 * Produces a copyable snippet for the current model + params + messages in four
 * targets. The SDK snippets MUST match the SHIPPED surfaces of the 10.2 / 10.3 /
 * 10.4 SDKs (presentation-layer fidelity — this is the consumer of those Stories,
 * not a re-spec):
 *   - Python (10.2):     from he_api import Client / client.chat.completions.create(...)
 *   - TypeScript (10.3): import { Client } from "@he-api/sdk" / client.chat.completions.create(...)
 *   - Go (10.4):         heapi.NewClient() / client.Chat.Completions.New(ctx, ...)
 *   - cURL:              POST /v1/chat/completions + Authorization: Bearer
 *
 * Pure + deterministic — no I/O, no secrets (the key is referenced via the
 * $HE_API_KEY env placeholder, never an actual key).
 */

export type ExportLang = 'curl' | 'python' | 'typescript' | 'go';

export interface ExportParams {
  model: string;
  /** Optional system message (prepended as a system role). */
  system?: string;
  /** The user message (required for a meaningful snippet). */
  user: string;
  temperature?: number;
  maxTokens?: number;
}

const BASE_URL = 'https://api.he-api.com/v1';

interface Message {
  role: 'system' | 'user';
  content: string;
}

function messagesOf(p: ExportParams): Message[] {
  const msgs: Message[] = [];
  if (p.system && p.system.trim() !== '') {
    msgs.push({ role: 'system', content: p.system });
  }
  msgs.push({ role: 'user', content: p.user });
  return msgs;
}

/** Build the canonical request-body object shared by cURL / the SDK calls. */
function bodyObject(p: ExportParams): Record<string, unknown> {
  const body: Record<string, unknown> = { model: p.model, messages: messagesOf(p) };
  if (typeof p.temperature === 'number') body.temperature = p.temperature;
  if (typeof p.maxTokens === 'number') body.max_tokens = p.maxTokens;
  return body;
}

function curlSnippet(p: ExportParams): string {
  const body = JSON.stringify(bodyObject(p), null, 2);
  return [
    `curl ${BASE_URL}/chat/completions \\`,
    `  -H "Authorization: Bearer $HE_API_KEY" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '${body}'`,
  ].join('\n');
}

function pyArgs(p: ExportParams): string {
  const lines = [
    `    model=${JSON.stringify(p.model)},`,
    `    messages=${JSON.stringify(messagesOf(p))},`,
  ];
  if (typeof p.temperature === 'number') lines.push(`    temperature=${p.temperature},`);
  if (typeof p.maxTokens === 'number') lines.push(`    max_tokens=${p.maxTokens},`);
  return lines.join('\n');
}

function pythonSnippet(p: ExportParams): string {
  return [
    'from he_api import Client',
    '',
    'client = Client()  # reads HE_API_KEY',
    'response = client.chat.completions.create(',
    pyArgs(p),
    ')',
    'print(response.choices[0].message.content)',
  ].join('\n');
}

function tsSnippet(p: ExportParams): string {
  return [
    'import { Client } from "@he-api/sdk";',
    '',
    'const client = new Client(); // reads HE_API_KEY',
    'const response = await client.chat.completions.create(',
    `  ${JSON.stringify(bodyObject(p), null, 2).replace(/\n/g, '\n  ')},`,
    ');',
    'console.log(response.choices[0].message.content);',
  ].join('\n');
}

function goSnippet(p: ExportParams): string {
  const msgLines = messagesOf(p)
    .map((m) =>
      m.role === 'system'
        ? `\t\t\topenai.SystemMessage(${JSON.stringify(m.content)}),`
        : `\t\t\topenai.UserMessage(${JSON.stringify(m.content)}),`,
    )
    .join('\n');
  const extra: string[] = [];
  if (typeof p.temperature === 'number') extra.push(`\t\tTemperature: openai.Float(${p.temperature}),`);
  if (typeof p.maxTokens === 'number') extra.push(`\t\tMaxTokens: openai.Int(${p.maxTokens}),`);
  return [
    'client := heapi.NewClient() // reads HE_API_KEY',
    'resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{',
    `\t\tModel: ${JSON.stringify(p.model)},`,
    '\t\tMessages: []openai.ChatCompletionMessageParamUnion{',
    msgLines,
    '\t\t},',
    ...extra,
    '})',
  ].join('\n');
}

/** Generate the export snippet for `lang`. */
export function generateExportSnippet(lang: ExportLang, params: ExportParams): string {
  switch (lang) {
    case 'curl':
      return curlSnippet(params);
    case 'python':
      return pythonSnippet(params);
    case 'typescript':
      return tsSnippet(params);
    case 'go':
      return goSnippet(params);
    default: {
      const _exhaustive: never = lang;
      return _exhaustive;
    }
  }
}

export const EXPORT_LANGS: readonly ExportLang[] = ['curl', 'python', 'typescript', 'go'];
