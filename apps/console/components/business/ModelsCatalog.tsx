'use client';

/**
 * ModelsCatalog — OpenRouter-style model marketplace grid. Client component so
 * the search box filters instantly. Fed the Zod-validated /public/models list
 * by the (marketing)/models server page. Labels are inline English (marketing
 * surface); no i18n namespace dependency.
 */
import { useMemo, useState } from 'react';

import type { ModelEntry } from '@/lib/api/public-models';

const PROVIDER_LABELS: Record<string, string> = {
  alibaba: 'Alibaba · Qwen',
  deepseek: 'DeepSeek',
  moonshot: 'Moonshot · Kimi',
  zhipu: 'Zhipu · GLM',
  bytedance: 'ByteDance · Doubao',
  baidu: 'Baidu · ERNIE',
};

function providerLabel(ownedBy: string): string {
  return PROVIDER_LABELS[ownedBy] ?? ownedBy.charAt(0).toUpperCase() + ownedBy.slice(1);
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 ? 1 : 0)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}K`;
  return String(n);
}

function capabilityTags(m: ModelEntry): string[] {
  const c = m.capabilities;
  const tags: string[] = [];
  if (c.chat) tags.push('Chat');
  if (c.streaming) tags.push('Streaming');
  if (c.function_calling) tags.push('Tools');
  if (c.vision) tags.push('Vision');
  if (c.json_mode) tags.push('JSON mode');
  if (c.transcription) tags.push('Transcription');
  if (c.speech) tags.push('Speech');
  return tags;
}

function ModelCard({ m }: { m: ModelEntry }) {
  return (
    <div className="group rounded-xl border border-slate-200 bg-white p-5 transition hover:border-blue-300 hover:shadow-md">
      <div className="flex items-start justify-between gap-3">
        <h3 className="text-lg font-semibold text-slate-900">{m.id}</h3>
        <span className="shrink-0 rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-medium text-slate-600">
          {providerLabel(m.owned_by)}
        </span>
      </div>

      <div className="mt-3 flex items-baseline gap-1.5">
        <span className="text-2xl font-bold text-slate-900">{formatTokens(m.capabilities.context_window_tokens)}</span>
        <span className="text-sm text-slate-500">context window</span>
      </div>
      <p className="mt-0.5 text-xs text-slate-500">
        Up to {formatTokens(m.capabilities.max_output_tokens)} output tokens
      </p>

      <div className="mt-4 flex flex-wrap gap-1.5">
        {capabilityTags(m).map((t) => (
          <span
            key={t}
            className="rounded-md bg-blue-50 px-2 py-1 text-xs font-medium text-blue-700"
          >
            {t}
          </span>
        ))}
      </div>
    </div>
  );
}

export function ModelsCatalog({ models }: { models: ModelEntry[] }) {
  const [query, setQuery] = useState('');

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return models;
    return models.filter(
      (m) => m.id.toLowerCase().includes(q) || m.owned_by.toLowerCase().includes(q),
    );
  }, [query, models]);

  return (
    <div>
      <div className="mb-5">
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search models or providers…"
          aria-label="Search models"
          className="w-full max-w-md rounded-lg border border-slate-300 px-4 py-2.5 text-sm text-slate-900 outline-none transition focus:border-blue-500 focus:ring-2 focus:ring-blue-100"
        />
      </div>

      {filtered.length === 0 ? (
        <p className="text-sm text-slate-500">No models match “{query}”.</p>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((m) => (
            <ModelCard key={m.id} m={m} />
          ))}
        </div>
      )}
    </div>
  );
}
