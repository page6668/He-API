'use client';

/**
 * 文档正文。内容源见 page.tsx 头注(全部取自真实代码,不杜撰)。
 * 视觉:knowledge/taste/design-system.md —— 限宽双栏(公开页 prose-page 1120)、
 * 暖边框零阴影、代码块 surface_sunken 内嵌底、朱砂只落一处(本页给「去模型广场试」)。
 */
import { useState } from 'react';

import { Panel, Button } from '@/components/ui/kit';

const BASE_URL = 'https://api.he-api.com/v1';

type Lang = 'curl' | 'python' | 'typescript' | 'go';
const LANGS: { id: Lang; label: string }[] = [
  { id: 'curl', label: 'cURL' },
  { id: 'python', label: 'Python' },
  { id: 'typescript', label: 'TypeScript' },
  { id: 'go', label: 'Go' },
];

const SNIPPETS: Record<Lang, string> = {
  curl: `curl ${BASE_URL}/chat/completions \\
  -H "Authorization: Bearer $HE_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
  "model": "qwen-max",
  "messages": [
    { "role": "user", "content": "你好" }
  ]
}'`,
  python: `# pip install openai
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["HE_API_KEY"],
    base_url="${BASE_URL}",
)

resp = client.chat.completions.create(
    model="qwen-max",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`,
  typescript: `// npm i openai
import OpenAI from 'openai';

const client = new OpenAI({
  apiKey: process.env.HE_API_KEY,
  baseURL: '${BASE_URL}',
});

const resp = await client.chat.completions.create({
  model: 'qwen-max',
  messages: [{ role: 'user', content: '你好' }],
});
console.log(resp.choices[0].message.content);`,
  go: `// go get github.com/he-api/he-api/packages/sdk-go
client := heapi.NewClient(os.Getenv("HE_API_KEY"))

resp, err := client.Chat.Completions.New(ctx, heapi.ChatRequest{
    Model: "qwen-max",
    Messages: []heapi.Message{{Role: "user", Content: "你好"}},
})`,
};

/** 真实路由,取自 apps/api-gateway/cmd/server/main.go 的注册表。 */
const ENDPOINTS: { method: string; path: string; en: string; zh: string }[] = [
  { method: 'POST', path: '/v1/chat/completions', en: 'Chat completion (streaming supported)', zh: '对话补全(支持流式)' },
  { method: 'GET', path: '/v1/models', en: 'List available models', zh: '列出可用模型' },
  { method: 'POST', path: '/v1/embeddings', en: 'Text embeddings', zh: '文本向量' },
  { method: 'POST', path: '/v1/audio/transcriptions', en: 'Speech to text', zh: '语音转文字' },
  { method: 'POST', path: '/v1/audio/speech', en: 'Text to speech', zh: '文字转语音' },
  { method: 'GET', path: '/v1/balance', en: 'Account balance', zh: '账户余额' },
];

/** 真实错误码,取自 internal/openaierr/codes.go。 */
const ERRORS: { code: string; en: string; zh: string }[] = [
  { code: '401_invalid_api_key', en: 'API key missing, malformed or revoked.', zh: 'API 密钥缺失、格式错误或已撤销。' },
  { code: '402_balance_insufficient', en: 'Account balance cannot cover this request.', zh: '账户余额不足以支付本次请求。' },
  { code: '403_model_not_in_scope', en: 'This key is not allowed to call that model.', zh: '该密钥无权调用此模型。' },
  { code: '403_ip_not_whitelisted', en: 'Caller IP is outside the key IP whitelist.', zh: '调用方 IP 不在密钥白名单内。' },
  { code: '429_rate_limit_rpm', en: 'Requests-per-minute ceiling reached.', zh: '已达每分钟请求数上限。' },
  { code: '429_rate_limit_tpm', en: 'Tokens-per-minute ceiling reached.', zh: '已达每分钟 Token 上限。' },
  { code: '400_content_filter', en: 'Content blocked by the safety filter.', zh: '内容被安全过滤拦截。' },
];

const SECTIONS = [
  { id: 'quickstart', en: 'Quickstart', zh: '快速开始' },
  { id: 'auth', en: 'Authentication', zh: '鉴权' },
  { id: 'endpoints', en: 'Endpoints', zh: '接口' },
  { id: 'streaming', en: 'Streaming', zh: '流式输出' },
  { id: 'errors', en: 'Errors', zh: '错误码' },
];

function Code({ children }: { children: string }) {
  return (
    <pre
      dir="ltr"
      className="mt-3 overflow-x-auto rounded-md border border-line bg-surface-sunken p-4 font-mono text-small leading-relaxed text-ink"
    >
      {children}
    </pre>
  );
}

export function DocsContent({ locale }: { locale: string }) {
  const zh = locale.startsWith('zh');
  const [lang, setLang] = useState<Lang>('curl');
  const T = (en: string, cn: string) => (zh ? cn : en);

  return (
    <div className="mx-auto max-w-prose-page px-6 py-20 lg:px-8">
      <header className="mb-12 max-w-2xl">
        <h1 className="text-display">{T('API Reference', 'API 文档')}</h1>
        <p className="mt-3 text-body text-ink-secondary">
          {T(
            'One OpenAI-compatible API for Qwen, DeepSeek, Doubao, ERNIE, GLM and Kimi.',
            '一个 OpenAI 兼容接口,调用通义千问、DeepSeek、豆包、文心、GLM 与 Kimi。',
          )}
        </p>
      </header>

      <div className="grid gap-8 lg:grid-cols-[180px_minmax(0,1fr)]">
        {/* 侧栏目录 */}
        <nav aria-label={T('On this page', '本页目录')} className="hidden lg:block">
          <ul className="sticky top-8 space-y-1.5">
            {SECTIONS.map((s) => (
              <li key={s.id}>
                <a
                  href={`#${s.id}`}
                  className="block text-small text-ink-secondary transition-colors duration-state ease-he hover:text-ink"
                >
                  {T(s.en, s.zh)}
                </a>
              </li>
            ))}
          </ul>
        </nav>

        <div className="min-w-0 space-y-10">
          {/* 快速开始 */}
          <section id="quickstart" className="scroll-mt-8">
            <h2 className="text-h2">{T('Quickstart', '快速开始')}</h2>
            <p className="mt-2 text-body text-ink-secondary">
              {T(
                'Create an API key in the console, then point any OpenAI-compatible client at the base URL below.',
                '在控制台创建 API 密钥,然后把任意 OpenAI 兼容客户端的 base URL 指向下面的地址即可。',
              )}
            </p>
            <Panel inset className="mt-3">
              <p className="text-label text-ink-muted">Base URL</p>
              <p className="tabular mt-1 text-metric text-ink">{BASE_URL}</p>
            </Panel>

            <div className="mt-5">
              <div role="tablist" aria-label={T('Language', '语言')} className="flex gap-1">
                {LANGS.map((l) => (
                  <button
                    key={l.id}
                    role="tab"
                    aria-selected={lang === l.id}
                    onClick={() => setLang(l.id)}
                    className={`rounded-md px-2.5 py-1 text-label transition-colors duration-state ease-he ${
                      lang === l.id ? 'bg-ink text-paper' : 'text-ink-secondary hover:bg-surface-sunken'
                    }`}
                  >
                    {l.label}
                  </button>
                ))}
              </div>
              <Code>{SNIPPETS[lang]}</Code>
            </div>

            <div className="mt-4">
              {/* 本页唯一的朱砂 */}
              <a href={`/${locale}/playground`}>
                <Button variant="primary">{T('Try it in the Playground', '去模型广场试一下')}</Button>
              </a>
            </div>
          </section>

          {/* 鉴权 */}
          <section id="auth" className="scroll-mt-8">
            <h2 className="text-h2">{T('Authentication', '鉴权')}</h2>
            <p className="mt-2 text-body text-ink-secondary">
              {T(
                'Every request carries your key in the Authorization header. Keys are created and revoked in the console; a revoked key fails immediately.',
                '每次请求都在 Authorization 头里携带密钥。密钥在控制台创建与撤销;已撤销的密钥会立即失效。',
              )}
            </p>
            <Code>{`Authorization: Bearer $HE_API_KEY`}</Code>
            <p className="mt-2 text-small text-ink-muted">
              {T(
                'Never ship a key in browser code — call from your server.',
                '不要把密钥放进浏览器端代码 —— 请从你的服务端调用。',
              )}
            </p>
          </section>

          {/* 接口 */}
          <section id="endpoints" className="scroll-mt-8">
            <h2 className="text-h2">{T('Endpoints', '接口')}</h2>
            <Panel padded={false} className="mt-3 overflow-hidden">
              <table className="w-full text-start text-small">
                <thead className="border-b border-line bg-surface-sunken text-start">
                  <tr>
                    <th scope="col" className="px-4 py-2 text-start text-label font-medium text-ink-muted">
                      {T('Method', '方法')}
                    </th>
                    <th scope="col" className="px-4 py-2 text-start text-label font-medium text-ink-muted">
                      {T('Path', '路径')}
                    </th>
                    <th scope="col" className="px-4 py-2 text-start text-label font-medium text-ink-muted">
                      {T('Description', '说明')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {ENDPOINTS.map((e, i) => (
                    <tr key={e.path} className={i > 0 ? 'border-t border-line' : ''}>
                      <td className="whitespace-nowrap px-4 py-3 align-top">
                        <span className="tabular text-label text-ink-muted">{e.method}</span>
                      </td>
                      <td className="px-4 py-3 align-top">
                        <span className="font-mono text-small text-ink">{e.path}</span>
                      </td>
                      <td className="px-4 py-3 align-top text-ink-secondary">{T(e.en, e.zh)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Panel>
          </section>

          {/* 流式 */}
          <section id="streaming" className="scroll-mt-8">
            <h2 className="text-h2">{T('Streaming', '流式输出')}</h2>
            <p className="mt-2 text-body text-ink-secondary">
              {T(
                'Set "stream": true to receive server-sent events. Each frame is a data: line; the stream ends with data: [DONE].',
                '设置 "stream": true 即可接收 SSE 事件流。每帧是一行 data:,流以 data: [DONE] 结束。',
              )}
            </p>
            <Code>{`data: {"choices":[{"delta":{"content":"你"}}]}
data: {"choices":[{"delta":{"content":"好"}}]}
data: [DONE]`}</Code>
          </section>

          {/* 错误码 */}
          <section id="errors" className="scroll-mt-8">
            <h2 className="text-h2">{T('Errors', '错误码')}</h2>
            <p className="mt-2 text-body text-ink-secondary">
              {T(
                'Errors return an OpenAI-compatible envelope. Read error.code — it is stable; the message is not.',
                '错误返回 OpenAI 兼容结构。请以 error.code 为准 —— 它是稳定的,message 不是。',
              )}
            </p>
            <Code>{`{
  "error": {
    "code": "401_invalid_api_key",
    "message": "Invalid API key.",
    "type": "invalid_request_error",
    "he_request_id": "req_5d873b805a8e"
  }
}`}</Code>
            <Panel padded={false} className="mt-4 overflow-hidden">
              <table className="w-full text-start text-small">
                <thead className="border-b border-line bg-surface-sunken text-start">
                  <tr>
                    <th scope="col" className="px-4 py-2 text-start text-label font-medium text-ink-muted">
                      {T('Code', '错误码')}
                    </th>
                    <th scope="col" className="px-4 py-2 text-start text-label font-medium text-ink-muted">
                      {T('Description', '说明')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {ERRORS.map((e, i) => (
                    <tr key={e.code} className={i > 0 ? 'border-t border-line' : ''}>
                      <td className="whitespace-nowrap px-4 py-3 align-top">
                        <span className="font-mono text-small text-ink">{e.code}</span>
                      </td>
                      <td className="px-4 py-3 align-top text-ink-secondary">{T(e.en, e.zh)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Panel>
            <p className="mt-3 text-small text-ink-muted">
              {T(
                'Include he_request_id when contacting support — it pins the exact request.',
                '联系支持时请附上 he_request_id —— 它能精确定位这次请求。',
              )}
            </p>
          </section>
        </div>
      </div>
    </div>
  );
}
