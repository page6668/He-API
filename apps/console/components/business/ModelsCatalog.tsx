'use client';

/**
 * ModelsCatalog — OpenRouter-style model marketplace grid. Client component so
 * the search box filters instantly. Fed the Zod-validated /public/models list
 * by the (marketing)/models server page. Labels are inline English (marketing
 * surface); no i18n namespace dependency.
 *
 * 视觉:knowledge/taste/design-system.md(「宣纸·墨·朱砂印」)。基元取自 components/ui/kit。
 * 铁律1 —— 上下文/输出 Token 走 .tabular,模型 id 走 font-mono;正文不等宽。
 * 铁律2 —— 本组件不含朱砂:能力标签是墨色描边药丸,层级只靠 1px 暖边框与明度差(零阴影)。
 */
import { useMemo, useState } from 'react';

import { Panel, fieldCls } from '@/components/ui/kit';
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

const CURRENCY_SYMBOLS: Record<string, string> = { USD: '$', CNY: '¥' };

/**
 * 展示用价格格式化。**纯字符串操作** —— 网关特意让金额从 postgres NUMERIC 一路
 * 以精确十进制字符串抵达前端,这里 parseFloat 一次就把那份精确性还回去了。
 * 只做两件事:去掉尾随 0(0.000880 → 0.00088)、加币种符号。
 */
function formatPrice(amount: string, currency: string): string {
  const symbol = CURRENCY_SYMBOLS[currency] ?? `${currency} `;
  const trimmed = amount.includes('.')
    ? amount.replace(/0+$/, '').replace(/\.$/, '')
    : amount;
  return `${symbol}${trimmed}`;
}

function ModelCard({ m }: { m: ModelEntry }) {
  return (
    // 零阴影 —— hover 只加深边框(design-system motion.use_where)
    <Panel className="transition-colors duration-state ease-he hover:border-line-strong">
      <div className="flex items-start justify-between gap-3">
        {/* 模型 id 是技术标识 → 等宽(brand.md naming.models) */}
        <h3 className="text-h3 font-mono text-ink">{m.id}</h3>
        <span className="shrink-0 rounded-full bg-surface-sunken px-2.5 py-0.5 text-label text-ink-secondary">
          {providerLabel(m.owned_by)}
        </span>
      </div>

      {/* 仪表读数:大号等宽 tabular 数字 + 小标签 */}
      <div className="mt-3 flex items-baseline gap-1.5">
        <span className="tabular text-metric-lg text-ink">
          {formatTokens(m.capabilities.context_window_tokens)}
        </span>
        <span className="text-small text-ink-secondary">context window</span>
      </div>
      <p className="mt-1 text-label text-ink-muted">
        Up to <span className="tabular">{formatTokens(m.capabilities.max_output_tokens)}</span> output
        tokens
      </p>

      {/* 价格 —— 网关产品的核心决策信息(AD-002)。铁律1:金额走 .tabular 等宽。
          无定价时整块不渲染:显示 0 会被读成「免费」。 */}
      {m.pricing && (
        <dl className="mt-3 flex gap-5 border-t border-line pt-3">
          <div>
            <dt className="text-label text-ink-muted">Input / 1K</dt>
            <dd className="tabular text-body text-ink">
              {formatPrice(m.pricing.input_per_1k_tokens, m.pricing.currency)}
            </dd>
          </div>
          <div>
            <dt className="text-label text-ink-muted">Output / 1K</dt>
            <dd className="tabular text-body text-ink">
              {formatPrice(m.pricing.output_per_1k_tokens, m.pricing.currency)}
            </dd>
          </div>
        </dl>
      )}

      {/* 能力标签:墨色系描边药丸(不用彩色块分区) */}
      <div className="mt-4 flex flex-wrap gap-1.5">
        {capabilityTags(m).map((t) => (
          <span
            key={t}
            className="rounded-md border border-line px-2 py-0.5 text-label text-ink-secondary"
          >
            {t}
          </span>
        ))}
      </div>
    </Panel>
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
          className={`${fieldCls} max-w-md`}
        />
      </div>

      {filtered.length === 0 ? (
        <p className="text-small text-ink-muted">No models match “{query}”.</p>
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
