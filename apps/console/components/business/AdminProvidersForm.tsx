'use client';

/**
 * AD-004 —— LLM Provider 配置表单。
 *
 * 6 个 provider（deepseek/doubao/ernie/glm/kimi/qwen）各一张卡片：
 *   - API Key 输入框（留空 = 不改现有 key）
 *   - Base URL 输入框（预填默认值）
 *   - 启用/停用开关
 *   - 逐行保存按钮
 *
 * 铁律：API Key 明文绝不在前端长期持有——输入框只在用户主动粘贴时获取值，
 * 保存后立即清空。List 返回的是 masked key（sk-***abcd），仅做展示。
 *
 * 视觉与 AdminPricingForm 同模式：密集行 + Panel + 唯一朱砂主操作（保存按钮）。
 */
import { useState } from 'react';

import { Badge, Button, fieldCls, labelCls, Panel } from '@/components/ui/kit';
import { updateProvider } from '@/app/[locale]/(console)/admin/providers/_actions/provider-actions';

// Provider 显示名称映射
const PROVIDER_DISPLAY: Record<string, string> = {
  deepseek: 'DeepSeek',
  doubao: '豆包（火山方舟）',
  ernie: '文心一言（百度）',
  glm: '智谱 GLM',
  kimi: 'Kimi（月之暗面）',
  qwen: '通义千问（阿里）',
};

export interface ProviderRowModel {
  name: string;
  baseURL: string;
  enabled: boolean;
  maskedKey: string;
  hasKey: boolean;
  updatedAt: string | null;
  verifiedAt: string | null;
  lastError: string | null;
}

export interface ProviderLabels {
  apiKeyLabel: string;
  apiKeyPlaceholder: string;
  baseURLLabel: string;
  enabledLabel: string;
  save: string;
  saving: string;
  saved: string;
  errInvalid: string;
  errForbidden: string;
  errUnauthorized: string;
  errNotFound: string;
  errGeneric: string;
  statusEnabled: string;
  statusDisabled: string;
  statusHasKey: string;
  statusNoKey: string;
  lastVerified: string;
  lastError: string;
  notVerified: string;
}

type RowStatus =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved' }
  | { kind: 'error'; message: string };

export function AdminProvidersForm({
  providers,
  labels,
}: {
  providers: ProviderRowModel[];
  labels: ProviderLabels;
}) {
  return (
    <Panel padded={false}>
      <div className="divide-y divide-line">
        {providers.map((p) => (
          <ProviderCard key={p.name} provider={p} labels={labels} />
        ))}
      </div>
    </Panel>
  );
}

function ProviderCard({
  provider,
  labels,
}: {
  provider: ProviderRowModel;
  labels: ProviderLabels;
}) {
  const [apiKey, setApiKey] = useState('');
  const [baseURL, setBaseURL] = useState(provider.baseURL);
  const [enabled, setEnabled] = useState(provider.enabled);
  const [status, setStatus] = useState<RowStatus>({ kind: 'idle' });

  const displayName = PROVIDER_DISPLAY[provider.name] ?? provider.name;

  async function handleSave() {
    setStatus({ kind: 'saving' });
    const result = await updateProvider({
      providerName: provider.name,
      apiKey: apiKey.trim(),
      baseURL: baseURL.trim(),
      enabled,
    });
    if (result.kind === 'ok') {
      setStatus({ kind: 'saved' });
      setApiKey(''); // 保存后清空 key 输入框
      setTimeout(() => setStatus({ kind: 'idle' }), 3000);
    } else if (result.kind === 'validation') {
      setStatus({ kind: 'error', message: result.message });
    } else if (result.kind === 'forbidden') {
      setStatus({ kind: 'error', message: labels.errForbidden });
    } else if (result.kind === 'unauthorized') {
      setStatus({ kind: 'error', message: labels.errUnauthorized });
    } else if (result.kind === 'not_found') {
      setStatus({ kind: 'error', message: labels.errNotFound });
    } else {
      setStatus({ kind: 'error', message: labels.errGeneric });
    }
  }

  return (
    <div className="px-6 py-5">
      {/* ── 头部：名称 + 状态徽章 ── */}
      <div className="mb-4 flex items-center gap-3">
        <h3 className="text-h3 text-ink">{displayName}</h3>
        <span className="font-mono text-label text-ink-muted">{provider.name}</span>
        {provider.enabled ? (
          <Badge tone="success">{labels.statusEnabled}</Badge>
        ) : (
          <Badge tone="neutral">{labels.statusDisabled}</Badge>
        )}
        {provider.hasKey ? (
          <Badge tone="info">{labels.statusHasKey}</Badge>
        ) : (
          <Badge tone="warning">{labels.statusNoKey}</Badge>
        )}
      </div>

      {/* ── masked key 展示 ── */}
      {provider.hasKey && provider.maskedKey && (
        <p className="mb-3 font-mono text-label text-ink-muted">
          当前 Key: {provider.maskedKey}
        </p>
      )}

      {/* ── 表单字段 ── */}
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label className={labelCls}>{labels.apiKeyLabel}</label>
          <input
            type="password"
            className={fieldCls}
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={labels.apiKeyPlaceholder}
            autoComplete="off"
          />
        </div>
        <div>
          <label className={labelCls}>{labels.baseURLLabel}</label>
          <input
            type="text"
            className={`${fieldCls} font-mono`}
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
          />
        </div>
      </div>

      {/* ── 启用开关 ── */}
      <div className="mt-4 flex items-center gap-2">
        <input
          type="checkbox"
          id={`enabled-${provider.name}`}
          className="h-4 w-4 rounded border-line-strong accent-seal"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <label htmlFor={`enabled-${provider.name}`} className="text-small text-ink-secondary">
          {labels.enabledLabel}
        </label>
      </div>

      {/* ── 连通性验证信息 ── */}
      {provider.verifiedAt && (
        <p className="mt-2 text-label text-ink-muted">
          {labels.lastVerified}: {new Date(provider.verifiedAt).toLocaleString()}
        </p>
      )}
      {provider.lastError && (
        <p className="mt-1 text-label text-crimson">
          {labels.lastError}: {provider.lastError}
        </p>
      )}

      {/* ── 操作区 ── */}
      <div className="mt-4 flex items-center gap-3">
        <Button
          variant="primary"
          onClick={handleSave}
          disabled={status.kind === 'saving'}
        >
          {status.kind === 'saving' ? labels.saving : labels.save}
        </Button>
        {status.kind === 'saved' && (
          <span className="text-small text-jade">{labels.saved}</span>
        )}
        {status.kind === 'error' && (
          <span className="text-small text-crimson">{status.message}</span>
        )}
      </div>
    </div>
  );
}
