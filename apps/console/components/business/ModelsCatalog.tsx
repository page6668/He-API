'use client';

/**
 * ModelsCatalog — OpenRouter-style dense model list (M2:卡片网格 → 密集列表行)。
 * Client component so the search box / sort select work instantly on the data
 * already fed by the (marketing)/models server page — no extra backend calls.
 * Labels are inline English (marketing surface); no i18n namespace dependency.
 *
 * 视觉:knowledge/taste/design-system.md(「宣纸·墨·朱砂印」key_page_direction.models)。
 * 基元取自 components/ui/kit(Panel/Badge/fieldCls,M1 统一后的共享层,只复用不另造)。
 * 铁律1 —— 上下文/输出 Token/价格走 .tabular(Plex Mono + tabular-nums)且行尾右对齐;
 *          模型 id 走 font-mono(brand.md naming.models);正文不等宽。
 * 铁律2 —— 本组件不含朱砂:focus ring 由 fieldCls 统一落朱砂环;厂商徽章走 Badge
 *          neutral(灰阶信息层),层级只靠 1px 暖边框与明度差(零阴影)。
 */
import { useMemo, useState } from 'react';

import { Badge, Panel, fieldCls } from '@/components/ui/kit';
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

/* ---------- 排序(仅客户端,仅用页面已有字段) ---------- */

type SortKey = 'default' | 'name' | 'context' | 'input-price' | 'output-price';

const SORT_OPTIONS: ReadonlyArray<{ value: SortKey; label: string }> = [
  { value: 'default', label: 'Default order' },
  { value: 'name', label: 'Name A–Z' },
  { value: 'context', label: 'Context window' },
  { value: 'input-price', label: 'Input price' },
  { value: 'output-price', label: 'Output price' },
];

/**
 * 排序比较键。注意:这里 Number() 只用于**排序比较**,展示仍走 formatPrice 的
 * 精确字符串(M-1:money 展示路径不过 float,比较大小不涉及展示精度)。
 * 无定价的模型排到最后(Infinity),而不是被读成 0/免费混进最便宜档。
 */
function priceKey(m: ModelEntry, side: 'input' | 'output'): number {
  if (!m.pricing) return Number.POSITIVE_INFINITY;
  const raw = side === 'input' ? m.pricing.input_per_1k_tokens : m.pricing.output_per_1k_tokens;
  const n = Number(raw);
  return Number.isFinite(n) ? n : Number.POSITIVE_INFINITY;
}

function sortModels(models: ModelEntry[], sort: SortKey): ModelEntry[] {
  if (sort === 'default') return models;
  const sorted = [...models];
  switch (sort) {
    case 'name':
      sorted.sort((a, b) => a.id.localeCompare(b.id));
      break;
    case 'context':
      // 上下文窗口大者优先(选这个排序的人在找长上下文)。
      sorted.sort(
        (a, b) => b.capabilities.context_window_tokens - a.capabilities.context_window_tokens,
      );
      break;
    case 'input-price':
      sorted.sort((a, b) => priceKey(a, 'input') - priceKey(b, 'input'));
      break;
    case 'output-price':
      sorted.sort((a, b) => priceKey(a, 'output') - priceKey(b, 'output'));
      break;
  }
  return sorted;
}

/* ---------- 列表行 ---------- */

function MetricCell({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-[4.5rem] sm:text-end">
      <dt className="text-label text-ink-muted">{label}</dt>
      <dd className="tabular text-metric text-ink">{value}</dd>
    </div>
  );
}

function ModelRow({ m }: { m: ModelEntry }) {
  const tags = capabilityTags(m);
  return (
    // calm-dense 数据行:hover 只提一档纸色,零阴影(design-system motion.use_where)
    <li className="grid gap-x-6 gap-y-2 px-5 py-3.5 transition-colors duration-state ease-he hover:bg-paper lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center">
      <div className="min-w-0">
        {/* 厂商 + 模型名:徽章走 Badge neutral(信息层灰阶);id 是技术标识 → 等宽 */}
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <Badge>{providerLabel(m.owned_by)}</Badge>
          <h3 className="truncate font-mono text-small font-medium text-ink">{m.id}</h3>
        </div>
        {/* 描述行:能力标签(/public/models 无自然语言描述字段,能力即描述) */}
        {tags.length > 0 && (
          <p className="mt-1 text-label text-ink-secondary">{tags.join(' · ')}</p>
        )}
      </div>

      {/* 指标行:全 Plex Mono tabular,≥lg 右对齐可比(数字右对齐铁律)。
          无定价时价格格子整个不渲染 —— 显示 0 会被读成「免费」。 */}
      <dl className="flex flex-wrap items-baseline gap-x-6 gap-y-1.5 lg:justify-end">
        <MetricCell label="Context" value={formatTokens(m.capabilities.context_window_tokens)} />
        <MetricCell label="Max output" value={formatTokens(m.capabilities.max_output_tokens)} />
        {m.pricing && (
          <>
            <MetricCell
              label="Input / 1K"
              value={formatPrice(m.pricing.input_per_1k_tokens, m.pricing.currency)}
            />
            <MetricCell
              label="Output / 1K"
              value={formatPrice(m.pricing.output_per_1k_tokens, m.pricing.currency)}
            />
          </>
        )}
      </dl>
    </li>
  );
}

export function ModelsCatalog({ models }: { models: ModelEntry[] }) {
  const [query, setQuery] = useState('');
  const [sort, setSort] = useState<SortKey>('default');

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const filtered = q
      ? models.filter(
          (m) =>
            m.id.toLowerCase().includes(q) ||
            m.owned_by.toLowerCase().includes(q) ||
            providerLabel(m.owned_by).toLowerCase().includes(q),
        )
      : models;
    return sortModels(filtered, sort);
  }, [query, sort, models]);

  return (
    <div>
      {/* 工具条:搜索 + 排序,纯客户端过滤页面已有数据 */}
      <div className="mb-5 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="w-full sm:max-w-md">
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search models or providers…"
            aria-label="Search models"
            className={fieldCls}
          />
        </div>
        <div className="shrink-0 sm:w-48">
          <select
            value={sort}
            onChange={(e) => setSort(e.target.value as SortKey)}
            aria-label="Sort models"
            className={fieldCls}
          >
            {SORT_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </div>
      </div>

      {visible.length === 0 ? (
        <p className="text-small text-ink-muted">No models match “{query}”.</p>
      ) : (
        <Panel padded={false}>
          <ul className="divide-y divide-line">
            {visible.map((m) => (
              <ModelRow key={m.id} m={m} />
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}
