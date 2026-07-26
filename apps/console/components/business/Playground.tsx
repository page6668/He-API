'use client';

/**
 * Story 10.6 — interactive Playground (AC1). Authed power-user surface: the
 * browser holds NO plaintext key; requests go to the JWT-cookie-authed gateway
 * proxy POST /v1/me/playground/chat carrying the user's owned api_key_id.
 *
 * Covers: model/param controls, System/User editors, streaming Output, estimated
 * cost/latency/tokens (estimate ONLY — never the billed amount), A/B compare
 * (non-streaming, 2 distinct concrete models), Export (cURL/Python/TS/Go), and
 * "Run in Playground" deep-link prefill. i18n + ar RTL with LTR islands for code /
 * model ids / numbers (BR-10.6.7 / BR-10.1.6-8).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';

import { HE_MODELS } from '@/lib/catalogue/pricing';
import { LtrText } from '@/components/business/LtrText';
import { isRtlLocale } from '@/lib/i18n';
import { canSend, isValidMaxTokens, isValidTemperature } from '@/lib/playground/validation';
import { estimatePlaygroundCost } from '@/lib/playground/cost';
import { buildAbModels } from '@/lib/playground/ab';
import { generateExportSnippet, EXPORT_LANGS, type ExportLang } from '@/lib/playground/export-snippets';
import { parseDeepLinkFragment } from '@/lib/playground/deep-link';
import { sendPlaygroundChat } from '@/lib/playground/client';
import { Button, Panel, fieldCls, labelCls } from '@/components/ui/kit';

interface KeyOption {
  api_key_id: string;
  name: string;
}

interface Metrics {
  promptTokens: number;
  completionTokens: number;
  latencyMs: number;
  model: string;
}

export function Playground({ locale }: { locale: string }) {
  const t = useTranslations('playground');
  const rtl = isRtlLocale(locale);

  const [keys, setKeys] = useState<KeyOption[]>([]);
  const [apiKeyId, setApiKeyId] = useState('');
  const [model, setModel] = useState(HE_MODELS[0]?.id ?? '');
  const [modelB, setModelB] = useState(HE_MODELS[1]?.id ?? '');
  const [abMode, setAbMode] = useState(false);
  const [temperature, setTemperature] = useState(0.7);
  const [maxTokens, setMaxTokens] = useState(256);
  const [system, setSystem] = useState('');
  const [user, setUser] = useState('');
  const [output, setOutput] = useState('');
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [exportLang, setExportLang] = useState<ExportLang>('curl');
  const [copied, setCopied] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  // Deep-link prefill (BR-10.6.6) — parse the #fragment; code is inserted as
  // literal text (never eval'd). Invalid payloads are ignored with a notice.
  useEffect(() => {
    if (typeof window === 'undefined' || !window.location.hash) return;
    const pre = parseDeepLinkFragment(window.location.hash);
    if (!pre) {
      if (window.location.hash.includes('prefill=')) setNotice(t('deepLink.ignored'));
      return;
    }
    setModel(pre.model);
    if (pre.system !== undefined) setSystem(pre.system);
    if (pre.user !== undefined) setUser(pre.user);
    if (pre.temperature !== undefined) setTemperature(pre.temperature);
    if (pre.maxTokens !== undefined) setMaxTokens(pre.maxTokens);
    setNotice(t('deepLink.prefilled'));
  }, [t]);

  // List the user's keys (browser → gateway, JWT cookie). No plaintext ever.
  // On 401 (unauthenticated deep-link landing), redirect to sign-in PRESERVING
  // the deep link (path + #fragment) as return_to so it is restored post-auth
  // (BR-10.6.6 / 10.6-INT-018).
  useEffect(() => {
    let alive = true;
    const base = (process.env.NEXT_PUBLIC_HE_API_BASE ?? '').replace(/\/$/, '');
    fetch(`${base}/v1/me/keys`, { credentials: 'include' })
      .then((r) => {
        if (r.status === 401 && typeof window !== 'undefined') {
          const returnTo = encodeURIComponent(window.location.pathname + window.location.hash);
          window.location.href = `${base}/${locale}/signin?return_to=${returnTo}`;
          return { data: [] };
        }
        return r.ok ? r.json() : { data: [] };
      })
      .then((j: { data?: KeyOption[] }) => {
        if (!alive) return;
        const list = Array.isArray(j.data) ? j.data : [];
        setKeys(list);
        if (list[0]) setApiKeyId(list[0].api_key_id);
      })
      .catch(() => {
        /* fail-soft: no keys → the UI guides the user to create one */
      });
    return () => {
      alive = false;
    };
  }, [locale]);

  const exportSnippet = useMemo(
    () => generateExportSnippet(exportLang, { model, system, user, temperature, maxTokens }),
    [exportLang, model, system, user, temperature, maxTokens],
  );

  const costEstimate = useMemo(() => {
    if (!metrics) return null;
    return estimatePlaygroundCost(metrics.model, {
      prompt_tokens: metrics.promptTokens,
      completion_tokens: metrics.completionTokens,
    });
  }, [metrics]);

  const sendDisabled = sending || !canSend(user) || apiKeyId === '' || !isValidTemperature(temperature) || !isValidMaxTokens(maxTokens);

  const mapError = useCallback(
    (status: number, code: string): string => {
      switch (status) {
        case 401:
          return t('errors.unauthorized');
        case 403:
          return code === '403_model_not_in_scope' ? t('errors.modelScope') : t('errors.keyForbidden');
        case 429:
          return t('errors.rateLimited');
        case 402:
          return t('errors.balance');
        default:
          return t('errors.generic');
      }
    },
    [t],
  );

  const handleSend = useCallback(async () => {
    if (sendDisabled) return;
    setError(null);
    setNotice(null);
    setOutput('');
    setMetrics(null);
    setSending(true);
    const started = Date.now();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    let abModels: string | undefined;
    if (abMode) {
      const ab = buildAbModels([model, modelB]);
      if (!ab.ok) {
        setError(t('errors.generic'));
        setSending(false);
        return;
      }
      abModels = ab.header;
    }

    try {
      const res = await sendPlaygroundChat(
        { apiKeyId, model, system, user, temperature, maxTokens, stream: !abMode, abModels },
        ctrl.signal,
      );
      if (!res.ok) {
        let code = '';
        try {
          code = (await res.clone().json())?.error?.code ?? '';
        } catch {
          /* non-JSON error body */
        }
        setError(mapError(res.status, code));
        return;
      }

      const ct = res.headers.get('Content-Type') ?? '';
      if (ct.includes('text/event-stream') && res.body) {
        // SSE stream — append deltas as they arrive (graceful close on [DONE]).
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buf = '';
        let acc = '';
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          const frames = buf.split('\n\n');
          buf = frames.pop() ?? '';
          for (const frame of frames) {
            const line = frame.replace(/^data: ?/, '').trim();
            if (line === '' || line === '[DONE]') continue;
            try {
              const chunk = JSON.parse(line);
              const delta = chunk?.choices?.[0]?.delta?.content;
              if (typeof delta === 'string') {
                acc += delta;
                setOutput(acc);
              }
              if (chunk?.usage) {
                setMetrics({
                  promptTokens: chunk.usage.prompt_tokens ?? 0,
                  completionTokens: chunk.usage.completion_tokens ?? 0,
                  latencyMs: Date.now() - started,
                  model,
                });
              }
            } catch {
              /* ignore partial/non-JSON frame */
            }
          }
        }
        if (!metrics) {
          setMetrics((m) => m ?? { promptTokens: 0, completionTokens: 0, latencyMs: Date.now() - started, model });
        }
      } else {
        // Non-stream (single or A/B) — parse the JSON envelope.
        const json = await res.json();
        const choices = Array.isArray(json?.choices) ? json.choices : [];
        const text = choices
          .map((c: { message?: { content?: string }; x_he_model?: string; finish_reason?: string }) => {
            const who = c.x_he_model ? `[${c.x_he_model}] ` : '';
            const content = c.finish_reason === 'he_upstream_error' ? '(upstream error)' : c.message?.content ?? '';
            return `${who}${content}`;
          })
          .join('\n\n');
        setOutput(text);
        setMetrics({
          promptTokens: json?.usage?.prompt_tokens ?? 0,
          completionTokens: json?.usage?.completion_tokens ?? 0,
          latencyMs: Date.now() - started,
          model: json?.model ?? model,
        });
      }
    } catch (e) {
      if ((e as Error).name === 'AbortError') return;
      setError(t('errors.streamInterrupted'));
    } finally {
      setSending(false);
      abortRef.current = null;
    }
  }, [sendDisabled, abMode, model, modelB, apiKeyId, system, user, temperature, maxTokens, mapError, t, metrics]);

  const copySnippet = useCallback(() => {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      void navigator.clipboard.writeText(exportSnippet);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }, [exportSnippet]);

  // 样式基元统一取自 components/ui/kit(M1):fieldCls/labelCls、Panel、Button。
  // 数字一律 .tabular(等宽制表);focus 环全站朱砂 focus-visible。
  return (
    // 内容恒有最大宽度并居中 —— 治「撑满整屏」(design-system.md layout.container)
    <section
      dir={rtl ? 'rtl' : 'ltr'}
      data-testid="playground"
      className="mx-auto max-w-playground px-6 py-10 lg:px-8"
    >
      <header className="mb-6">
        <h1 className="text-h1">{t('page.heading')}</h1>
        <p className="mt-1 text-small text-ink-secondary">{t('page.subheading')}</p>
      </header>

      {notice && (
        <div
          role="status"
          data-testid="playground-notice"
          className="mb-4 rounded-lg border border-line bg-surface px-4 py-2.5 text-small text-ink-secondary"
        >
          {notice}
        </div>
      )}
      {keys.length === 0 && (
        <div
          role="alert"
          data-testid="playground-no-key"
          className="mb-4 rounded-lg border border-ochre/30 bg-ochre/5 px-4 py-2.5 text-small text-ochre"
        >
          {t('errors.noKey')}
        </div>
      )}

      {/* 双栏:左 480px 参数/输入 · 右 流式输出 + 代码导出 */}
      <div className="grid items-start gap-6 lg:grid-cols-[480px_minmax(0,1fr)]">
        <Panel className="space-y-4">
          {keys.length > 0 && (
            <label className="block">
              <span className={labelCls}>{t('controls.apiKey')}</span>
              <select
                data-testid="playground-apikey"
                className={fieldCls}
                value={apiKeyId}
                onChange={(e) => setApiKeyId(e.target.value)}
              >
                {keys.map((k) => (
                  <option key={k.api_key_id} value={k.api_key_id}>
                    {k.name || k.api_key_id}
                  </option>
                ))}
              </select>
            </label>
          )}

          <label className="block">
            <span className={labelCls}>{abMode ? t('controls.modelA') : t('controls.model')}</span>
            {/* 模型 id 是技术标识 → 等宽 */}
            <select
              data-testid="playground-model"
              className={`${fieldCls} font-mono`}
              value={model}
              onChange={(e) => setModel(e.target.value)}
            >
              {HE_MODELS.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id}
                </option>
              ))}
            </select>
          </label>

          <label className="flex items-center gap-2 text-small text-ink-secondary">
            <input
              type="checkbox"
              data-testid="playground-ab-toggle"
              checked={abMode}
              onChange={(e) => setAbMode(e.target.checked)}
              className="h-4 w-4 rounded-sm border-line-strong text-ink accent-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30"
            />
            <span>{t('controls.compareAB')}</span>
          </label>

          {abMode && (
            <label className="block">
              <span className={labelCls}>{t('controls.modelB')}</span>
              <select
                data-testid="playground-modelB"
                className={`${fieldCls} font-mono`}
                value={modelB}
                onChange={(e) => setModelB(e.target.value)}
              >
                {HE_MODELS.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id}
                  </option>
                ))}
              </select>
            </label>
          )}

          <div className="grid grid-cols-2 gap-4">
            <label className="block">
              <span className={labelCls}>{t('controls.temperature')}</span>
              <input
                type="number"
                data-testid="playground-temperature"
                min={0}
                max={2}
                step={0.1}
                className={`${fieldCls} tabular`}
                value={temperature}
                onChange={(e) => setTemperature(Number(e.target.value))}
              />
            </label>
            <label className="block">
              <span className={labelCls}>{t('controls.maxTokens')}</span>
              <input
                type="number"
                data-testid="playground-maxtokens"
                min={1}
                step={1}
                className={`${fieldCls} tabular`}
                value={maxTokens}
                onChange={(e) => setMaxTokens(Number(e.target.value))}
              />
            </label>
          </div>

          <label className="block">
            <span className={labelCls}>{t('controls.system')}</span>
            <textarea
              data-testid="playground-system"
              className={`${fieldCls} resize-y`}
              rows={2}
              value={system}
              onChange={(e) => setSystem(e.target.value)}
            />
          </label>
          <label className="block">
            <span className={labelCls}>{t('controls.user')}</span>
            <textarea
              data-testid="playground-user"
              className={`${fieldCls} resize-y`}
              rows={5}
              placeholder={t('controls.userPlaceholder')}
              value={user}
              onChange={(e) => setUser(e.target.value)}
            />
          </label>

          {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2) */}
          <Button
            variant="primary"
            data-testid="playground-send"
            disabled={sendDisabled}
            onClick={handleSend}
            className="w-full"
          >
            {sending ? t('controls.sending') : t('controls.send')}
          </Button>
        </Panel>

        <div className="space-y-4">
          <Panel>
            <h2 className="text-h3">{t('output.heading')}</h2>
            {error && (
              <div
                role="alert"
                data-testid="playground-error"
                className="mt-3 rounded-md border border-crimson/30 bg-crimson/5 px-3 py-2 text-small text-crimson"
              >
                {error}
              </div>
            )}
            {/* 内嵌区 + 等宽:模型输出是"读数",不是正文 */}
            <pre
              data-testid="playground-output"
              aria-live="polite"
              className="mt-3 min-h-[14rem] whitespace-pre-wrap rounded-md border border-line bg-surface-sunken p-4 font-mono text-small leading-relaxed text-ink"
            >
              {output || <span className="text-ink-muted">{t('output.empty')}</span>}
            </pre>

            {metrics && (
              <dl
                className="mt-4 grid grid-cols-2 gap-x-6 gap-y-3 border-t border-line pt-4 sm:grid-cols-4"
                data-testid="playground-metrics"
              >
                <div>
                  <dt className="text-label text-ink-muted">{t('output.tokensIn')}</dt>
                  <dd className="tabular mt-0.5 text-metric text-ink">
                    <LtrText>{metrics.promptTokens}</LtrText>
                  </dd>
                </div>
                <div>
                  <dt className="text-label text-ink-muted">{t('output.tokensOut')}</dt>
                  <dd className="tabular mt-0.5 text-metric text-ink">
                    <LtrText>{metrics.completionTokens}</LtrText>
                  </dd>
                </div>
                <div>
                  <dt className="text-label text-ink-muted">{t('output.latency')}</dt>
                  <dd className="tabular mt-0.5 text-metric text-ink">
                    <LtrText>{metrics.latencyMs}ms</LtrText>
                  </dd>
                </div>
                <div>
                  <dt className="text-label text-ink-muted">{t('output.cost')}</dt>
                  <dd
                    className="mt-0.5 text-metric text-ink"
                    data-testid="playground-cost"
                    title={t('output.estimatedTitle')}
                  >
                    {costEstimate ? (
                      <span>
                        <span className="tabular">
                          <LtrText>${costEstimate.amountUsd.toFixed(6)}</LtrText>
                        </span>{' '}
                        <span className="text-label text-ink-muted">({t('output.estimated')})</span>
                      </span>
                    ) : (
                      <span className="text-ink-muted">—</span>
                    )}
                  </dd>
                </div>
              </dl>
            )}
          </Panel>

          <Panel>
            <div className="flex items-center justify-between gap-4">
              <h3 className="text-h3">{t('export.heading')}</h3>
              {/* 激活态用墨色而非朱砂 —— 每屏只允许一处朱砂(已给「发送」) */}
              <div role="tablist" aria-label={t('export.heading')} className="flex gap-1">
                {EXPORT_LANGS.map((lang) => (
                  <button
                    key={lang}
                    role="tab"
                    aria-selected={exportLang === lang}
                    data-testid={`playground-export-${lang}`}
                    onClick={() => setExportLang(lang)}
                    className={`rounded-md px-2.5 py-1 text-label transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 ${
                      exportLang === lang
                        ? 'bg-ink text-paper'
                        : 'text-ink-secondary hover:bg-surface-sunken'
                    }`}
                  >
                    {t(`export.${lang}` as 'export.curl')}
                  </button>
                ))}
              </div>
            </div>
            <pre
              data-testid="playground-snippet"
              dir="ltr"
              className="mt-3 overflow-x-auto rounded-md bg-ink p-4 font-mono text-small leading-relaxed text-paper"
            >
              {exportSnippet}
            </pre>
            <Button data-testid="playground-copy" onClick={copySnippet} className="mt-2">
              {copied ? t('export.copied') : t('export.copy')}
            </Button>
          </Panel>
        </div>
      </div>
    </section>
  );
}
