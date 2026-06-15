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

  return (
    <section dir={rtl ? 'rtl' : 'ltr'} data-testid="playground">
      <h1 className="text-2xl font-bold mb-1">{t('page.heading')}</h1>
      <p className="text-slate-600 mb-4">{t('page.subheading')}</p>

      {notice && (
        <div role="status" data-testid="playground-notice" className="mb-3 rounded border border-sky-300 bg-sky-50 px-3 py-2 text-sm text-sky-900">
          {notice}
        </div>
      )}
      {keys.length === 0 && (
        <div role="alert" data-testid="playground-no-key" className="mb-3 rounded border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900">
          {t('errors.noKey')}
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-3">
          {keys.length > 0 && (
            <label className="block text-sm">
              <span className="block font-medium">{t('controls.apiKey')}</span>
              <select data-testid="playground-apikey" className="mt-1 w-full rounded border px-2 py-1" value={apiKeyId} onChange={(e) => setApiKeyId(e.target.value)}>
                {keys.map((k) => (
                  <option key={k.api_key_id} value={k.api_key_id}>{k.name || k.api_key_id}</option>
                ))}
              </select>
            </label>
          )}

          <label className="block text-sm">
            <span className="block font-medium">{abMode ? t('controls.modelA') : t('controls.model')}</span>
            <select data-testid="playground-model" className="mt-1 w-full rounded border px-2 py-1" value={model} onChange={(e) => setModel(e.target.value)}>
              {HE_MODELS.map((m) => (
                <option key={m.id} value={m.id}>{m.id}</option>
              ))}
            </select>
          </label>

          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" data-testid="playground-ab-toggle" checked={abMode} onChange={(e) => setAbMode(e.target.checked)} />
            <span>{t('controls.compareAB')}</span>
          </label>

          {abMode && (
            <label className="block text-sm">
              <span className="block font-medium">{t('controls.modelB')}</span>
              <select data-testid="playground-modelB" className="mt-1 w-full rounded border px-2 py-1" value={modelB} onChange={(e) => setModelB(e.target.value)}>
                {HE_MODELS.map((m) => (
                  <option key={m.id} value={m.id}>{m.id}</option>
                ))}
              </select>
            </label>
          )}

          <div className="grid grid-cols-2 gap-3">
            <label className="block text-sm">
              <span className="block font-medium">{t('controls.temperature')}</span>
              <input type="number" data-testid="playground-temperature" min={0} max={2} step={0.1} className="mt-1 w-full rounded border px-2 py-1" value={temperature} onChange={(e) => setTemperature(Number(e.target.value))} />
            </label>
            <label className="block text-sm">
              <span className="block font-medium">{t('controls.maxTokens')}</span>
              <input type="number" data-testid="playground-maxtokens" min={1} step={1} className="mt-1 w-full rounded border px-2 py-1" value={maxTokens} onChange={(e) => setMaxTokens(Number(e.target.value))} />
            </label>
          </div>

          <label className="block text-sm">
            <span className="block font-medium">{t('controls.system')}</span>
            <textarea data-testid="playground-system" className="mt-1 w-full rounded border px-2 py-1" rows={2} value={system} onChange={(e) => setSystem(e.target.value)} />
          </label>
          <label className="block text-sm">
            <span className="block font-medium">{t('controls.user')}</span>
            <textarea data-testid="playground-user" className="mt-1 w-full rounded border px-2 py-1" rows={4} placeholder={t('controls.userPlaceholder')} value={user} onChange={(e) => setUser(e.target.value)} />
          </label>

          <button type="button" data-testid="playground-send" disabled={sendDisabled} onClick={handleSend} className="rounded bg-slate-900 px-4 py-2 text-white disabled:opacity-50">
            {sending ? t('controls.sending') : t('controls.send')}
          </button>
        </div>

        <div className="space-y-3">
          <h2 className="font-semibold">{t('output.heading')}</h2>
          {error && (
            <div role="alert" data-testid="playground-error" className="rounded border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-900">{error}</div>
          )}
          <pre data-testid="playground-output" aria-live="polite" className="min-h-[8rem] whitespace-pre-wrap rounded border bg-slate-50 p-3 text-sm">{output || t('output.empty')}</pre>

          {metrics && (
            <dl className="grid grid-cols-2 gap-2 text-sm" data-testid="playground-metrics">
              <div><dt className="text-slate-500">{t('output.tokensIn')}</dt><dd><LtrText>{metrics.promptTokens}</LtrText></dd></div>
              <div><dt className="text-slate-500">{t('output.tokensOut')}</dt><dd><LtrText>{metrics.completionTokens}</LtrText></dd></div>
              <div><dt className="text-slate-500">{t('output.latency')}</dt><dd><LtrText>{metrics.latencyMs}ms</LtrText></dd></div>
              <div>
                <dt className="text-slate-500">{t('output.cost')}</dt>
                <dd data-testid="playground-cost" title={t('output.estimatedTitle')}>
                  {costEstimate ? (
                    <span><LtrText>${costEstimate.amountUsd.toFixed(6)}</LtrText> <span className="text-xs text-slate-500">({t('output.estimated')})</span></span>
                  ) : (
                    '—'
                  )}
                </dd>
              </div>
            </dl>
          )}

          <div>
            <h3 className="text-sm font-semibold">{t('export.heading')}</h3>
            <div role="tablist" aria-label={t('export.heading')} className="mt-1 flex gap-2">
              {EXPORT_LANGS.map((lang) => (
                <button
                  key={lang}
                  role="tab"
                  aria-selected={exportLang === lang}
                  data-testid={`playground-export-${lang}`}
                  onClick={() => setExportLang(lang)}
                  className={`rounded border px-2 py-1 text-xs ${exportLang === lang ? 'bg-slate-900 text-white' : ''}`}
                >
                  {t(`export.${lang}` as 'export.curl')}
                </button>
              ))}
            </div>
            <pre data-testid="playground-snippet" dir="ltr" className="mt-2 overflow-x-auto rounded border bg-slate-900 p-3 text-xs text-slate-100">{exportSnippet}</pre>
            <button type="button" data-testid="playground-copy" onClick={copySnippet} className="mt-1 rounded border px-2 py-1 text-xs">
              {copied ? t('export.copied') : t('export.copy')}
            </button>
          </div>
        </div>
      </div>
    </section>
  );
}
