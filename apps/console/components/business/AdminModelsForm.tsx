'use client';

// AD-006 — 管理员模型管理表单组件。
//
// 展示模型列表（含 status badge），提供新增弹窗和下架操作。
// 与 AdminPricingForm / AdminProvidersForm 同模式。

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { createModel, deprecateModel, type ModelItem } from '@/app/[locale]/(console)/admin/models/_actions/models-actions';

export interface ModelRowModel {
  id: string;
  displayName: string;
  vendor: string;
  capabilities: Record<string, any>;
  upstreamModelId: string;
  status: 'active' | 'pending' | 'deprecated';
  createdAt: string;
  updatedAt: string | null;
}

export interface ModelLabels {
  vendorLabel: string;
  capabilitiesLabel: string;
  upstreamModelIdLabel: string;
  statusLabel: string;
  statusActive: string;
  statusPending: string;
  statusDeprecated: string;
  createTitle: string;
  createId: string;
  createIdPlaceholder: string;
  createDisplayName: string;
  createDisplayNamePlaceholder: string;
  createVendor: string;
  createVendorPlaceholder: string;
  createCapabilities: string;
  createUpstreamModelId: string;
  createUpstreamModelIdPlaceholder: string;
  create: string;
  creating: string;
  deprecate: string;
  deprecating: string;
  deprecateConfirmTitle: string;
  deprecateConfirmBody: string;
  cancel: string;
  save: string;
  saving: string;
  saved: string;
  errConflict: string;
  errValidation: string;
  errForbidden: string;
  errUnauthorized: string;
  errNotFound: string;
  errGeneric: string;
  emptyTitle: string;
  emptyBody: string;
}

// Capability badge config
const CAPS = [
  { key: 'chat', label: 'Chat' },
  { key: 'streaming', label: 'Streaming' },
  { key: 'function_calling', label: 'Function Calling' },
  { key: 'vision', label: 'Vision' },
  { key: 'json_mode', label: 'JSON Mode' },
  { key: 'transcription', label: 'Transcription' },
  { key: 'speech', label: 'Speech' },
] as const;

const VENDORS: Record<string, string> = {
  alibaba: '阿里云百炼',
  deepseek: 'DeepSeek',
  moonshot: 'Moonshot (Kimi)',
  zhipu: '智谱 GLM',
  bytedance: '字节豆包',
  baidu: '百度 ERNIE',
};

function StatusBadge({ status, labels }: { status: string; labels: ModelLabels }) {
  const map: Record<string, { cls: string; text: string }> = {
    active: { cls: 'bg-green-50 text-green-700 ring-green-600/20', text: labels.statusActive },
    pending: { cls: 'bg-yellow-50 text-yellow-700 ring-yellow-600/20', text: labels.statusPending },
    deprecated: { cls: 'bg-red-50 text-red-700 ring-red-600/20', text: labels.statusDeprecated },
  };
  const s = map[status] ?? { cls: 'bg-gray-50 text-gray-600', text: status };
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${s.cls}`}>
      {s.text}
    </span>
  );
}

function CapabilityBadges({ caps, labels }: { caps: Record<string, any>; labels: ModelLabels }) {
  const active = CAPS.filter((c) => caps[c.key]);
  if (active.length === 0) return <span className="text-xs text-gray-400">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {active.map((c) => (
        <span
          key={c.key}
          className="inline-flex items-center rounded bg-blue-50 px-1.5 py-0.5 text-xs font-medium text-blue-700"
        >
          {c.label}
        </span>
      ))}
    </div>
  );
}

// Create Modal
function CreateModal({
  onClose,
  labels,
}: {
  onClose: () => void;
  labels: ModelLabels;
}) {
  const [isPending, startTransition] = useTransition();
  const router = useRouter();
  const [id, setId] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [vendor, setVendor] = useState('');
  const [upstreamModelId, setUpstreamModelId] = useState('');
  const [chat, setChat] = useState(true);
  const [streaming, setStreaming] = useState(true);
  const [functionCalling, setFunctionCalling] = useState(false);
  const [vision, setVision] = useState(false);
  const [jsonMode, setJsonMode] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!id.trim() || !displayName.trim() || !vendor) {
      setError('请填写模型 ID、显示名称和厂商');
      return;
    }
    const caps = { chat, streaming, function_calling: functionCalling, vision, json_mode: jsonMode };
    startTransition(async () => {
      setError('');
      const result = await createModel({
        id: id.trim(),
        displayName: displayName.trim(),
        vendor,
        capabilities: caps,
        upstreamModelId: upstreamModelId.trim() || undefined,
      });
      if (result.kind === 'ok') {
        setSuccess(true);
        setTimeout(() => { router.refresh(); onClose(); }, 1200);
      } else if (result.kind === 'conflict') {
        setError(labels.errConflict);
      } else if (result.kind === 'validation') {
        setError(result.message);
      } else {
        setError(labels.errGeneric);
      }
    });
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl bg-surface shadow-2xl ring-1 ring-line">
        <div className="flex items-center justify-between border-b border-line px-5 py-4">
          <h2 className="text-base font-semibold text-strong">{labels.createTitle}</h2>
          <button onClick={onClose} className="text-2xl leading-none text-muted hover:text-strong">&times;</button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4 p-5">
          {/* Model ID */}
          <div>
            <label className="mb-1 block text-sm font-medium text-strong">{labels.createId} <span className="text-red-500">*</span></label>
            <input
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder={labels.createIdPlaceholder}
              className="w-full rounded-lg border border-line bg-paper px-3 py-2 text-sm text-strong placeholder-muted focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </div>

          {/* Display Name */}
          <div>
            <label className="mb-1 block text-sm font-medium text-strong">{labels.createDisplayName} <span className="text-red-500">*</span></label>
            <input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={labels.createDisplayNamePlaceholder}
              className="w-full rounded-lg border border-line bg-paper px-3 py-2 text-sm text-strong placeholder-muted focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </div>

          {/* Vendor */}
          <div>
            <label className="mb-1 block text-sm font-medium text-strong">{labels.createVendor} <span className="text-red-500">*</span></label>
            <select
              value={vendor}
              onChange={(e) => setVendor(e.target.value)}
              className="w-full rounded-lg border border-line bg-paper px-3 py-2 text-sm text-strong focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            >
              <option value="">{labels.createVendorPlaceholder}</option>
              {Object.entries(VENDORS).map(([k, v]) => (
                <option key={k} value={k}>{v}</option>
              ))}
            </select>
          </div>

          {/* Upstream Model ID */}
          <div>
            <label className="mb-1 block text-sm font-medium text-strong">{labels.createUpstreamModelId}</label>
            <input
              value={upstreamModelId}
              onChange={(e) => setUpstreamModelId(e.target.value)}
              placeholder={labels.createUpstreamModelIdPlaceholder}
              className="w-full rounded-lg border border-line bg-paper px-3 py-2 text-sm text-strong placeholder-muted focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </div>

          {/* Capabilities */}
          <div>
            <label className="mb-2 block text-sm font-medium text-strong">{labels.createCapabilities}</label>
            <div className="flex flex-wrap gap-2">
              {CAPS.map((c) => {
                const vals: Record<string, boolean> = { chat, streaming, function_calling: functionCalling, vision, json_mode: jsonMode };
                return (
                  <label key={c.key} className="flex items-center gap-1.5 text-sm text-muted">
                    <input
                      type="checkbox"
                      checked={!!vals[c.key]}
                      onChange={(e) => {
                        if (c.key === 'chat') setChat(e.target.checked);
                        if (c.key === 'streaming') setStreaming(e.target.checked);
                        if (c.key === 'function_calling') setFunctionCalling(e.target.checked);
                        if (c.key === 'vision') setVision(e.target.checked);
                        if (c.key === 'json_mode') setJsonMode(e.target.checked);
                      }}
                      className="h-3.5 w-3.5 rounded border-line text-primary focus:ring-primary"
                    />
                    {c.label}
                  </label>
                );
              })}
            </div>
          </div>

          {error && (
            <p role="alert" className="rounded bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>
          )}
          {success && (
            <p className="rounded bg-green-50 px-3 py-2 text-sm text-green-700">{labels.saved}</p>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="rounded-lg border border-line px-4 py-2 text-sm font-medium text-muted hover:bg-paper"
            >
              {labels.cancel}
            </button>
            <button
              type="submit"
              disabled={isPending || success}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-white hover:bg-primary/90 disabled:opacity-60"
            >
              {isPending ? labels.creating : labels.create}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// Deprecate Confirm Modal
function DeprecateModal({
  model,
  onClose,
  labels,
}: {
  model: ModelRowModel;
  onClose: () => void;
  labels: ModelLabels;
}) {
  const [isPending, startTransition] = useTransition();
  const router = useRouter();
  const [error, setError] = useState('');

  function handleConfirm() {
    startTransition(async () => {
      setError('');
      const result = await deprecateModel(model.id);
      if (result.kind === 'ok') {
        router.refresh();
        onClose();
      } else if (result.kind === 'not_found') {
        setError(labels.errNotFound);
      } else {
        setError(labels.errGeneric);
      }
    });
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-sm rounded-xl bg-surface shadow-2xl ring-1 ring-line">
        <div className="border-b border-line px-5 py-4">
          <h2 className="text-base font-semibold text-strong">{labels.deprecateConfirmTitle}</h2>
        </div>
        <div className="p-5">
          <p className="text-sm text-muted">
            {labels.deprecateConfirmBody.replace('{modelId}', model.id)}
          </p>
          {error && (
            <p role="alert" className="mt-3 rounded bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>
          )}
          <div className="mt-4 flex justify-end gap-2">
            <button
              onClick={onClose}
              className="rounded-lg border border-line px-4 py-2 text-sm font-medium text-muted hover:bg-paper"
            >
              {labels.cancel}
            </button>
            <button
              onClick={handleConfirm}
              disabled={isPending}
              className="rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-60"
            >
              {isPending ? labels.deprecating : labels.deprecate}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// Main component
export function AdminModelsForm({ models, labels }: { models: ModelRowModel[]; labels: ModelLabels }) {
  const [showCreate, setShowCreate] = useState(false);
  const [deprecateTarget, setDeprecateTarget] = useState<ModelRowModel | null>(null);

  return (
    <>
      {/* Header */}
      <div className="mb-4 flex items-center justify-between">
        <p className="text-sm text-muted">
          共 {models.length} 个模型
        </p>
        <button
          onClick={() => setShowCreate(true)}
          className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-white hover:bg-primary/90"
        >
          + {labels.createTitle}
        </button>
      </div>

      {/* Table */}
      <div className="overflow-x-auto rounded-xl ring-1 ring-line">
        <table className="min-w-full divide-y divide-line text-sm">
          <thead className="bg-paper">
            <tr>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.createId}</th>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.createDisplayName}</th>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.vendorLabel}</th>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.upstreamModelIdLabel}</th>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.capabilitiesLabel}</th>
              <th className="px-4 py-3 text-left font-medium text-muted">{labels.statusLabel}</th>
              <th className="px-4 py-3 text-right font-medium text-muted">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line bg-surface">
            {models.map((m) => (
              <tr key={m.id} className="hover:bg-paper/50">
                <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-primary">{m.id}</td>
                <td className="px-4 py-3 text-strong">{m.displayName}</td>
                <td className="whitespace-nowrap px-4 py-3 text-muted">{VENDORS[m.vendor] ?? m.vendor}</td>
                <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-muted">
                  {m.upstreamModelId || <span className="text-gray-400">—</span>}
                </td>
                <td className="px-4 py-3">
                  <CapabilityBadges caps={m.capabilities} labels={labels} />
                </td>
                <td className="whitespace-nowrap px-4 py-3">
                  <StatusBadge status={m.status} labels={labels} />
                </td>
                <td className="whitespace-nowrap px-4 py-3 text-right">
                  {m.status !== 'deprecated' && (
                    <button
                      onClick={() => setDeprecateTarget(m)}
                      className="rounded px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50"
                    >
                      {labels.deprecate}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Modals */}
      {showCreate && (
        <CreateModal onClose={() => setShowCreate(false)} labels={labels} />
      )}
      {deprecateTarget && (
        <DeprecateModal
          model={deprecateTarget}
          onClose={() => setDeprecateTarget(null)}
          labels={labels}
        />
      )}
    </>
  );
}
