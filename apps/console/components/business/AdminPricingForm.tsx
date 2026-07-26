'use client';

/**
 * AD-003 —— 管理员定价表单(specs/admin-pricing-arch.md)。
 *
 * 每个 active 模型一行:输入价、输出价(**元 / 百万 tokens**),逐行保存。
 * 页面顶部一个共享汇率(默认取 fx_rates 最新 USD→CNY,可手改)。
 *
 * 铁律:前端不做任何金额运算。填的字符串原样交给 Server Action → 网关 → SQL
 * (NUMERIC 换算)。这里只做「非空 + 正十进制」的即时反馈,真正的校验在网关。
 * 数字输入走 .tabular 等宽(设计系统铁律1)。
 *
 * M6-C 视觉:模型列表走密集行(单 Panel padded={false} + divide-y,同 ModelsCatalog);
 * 模型 id font-mono,现价参考走 .tabular(纯字符串拼接,不过 Number);
 * 「保存」是本页唯一朱砂主操作,focus ring 由 kit fieldCls/Button 统一落朱砂环。
 */
import { useState } from 'react';

import { Button, fieldCls, labelCls, Panel } from '@/components/ui/kit';
import { setModelPrice } from '@/app/[locale]/(console)/admin/pricing/_actions/pricing-actions';

const POSITIVE_DECIMAL = /^\d+(\.\d+)?$/;

function isPositiveDecimal(s: string): boolean {
  if (!POSITIVE_DECIMAL.test(s)) return false;
  return /[1-9]/.test(s); // 排除 0 / 0.00
}

export interface PricingRowModel {
  id: string;
  ownedBy: string;
  /** 当前对客户展示的单价(USD/1K),仅供参考,可为空。 */
  currentInputUsd?: string;
  currentOutputUsd?: string;
}

/** 表单全部文案,由服务端页面按当前 locale 传入(i18n 单一来源)。 */
export interface PricingLabels {
  fxLabel: string;
  fxHelp: string;
  inputLabel: string;
  outputLabel: string;
  inputPlaceholder: string;
  outputPlaceholder: string;
  save: string;
  saving: string;
  saved: string;
  errInvalid: string;
  errForbidden: string;
  errUnauthorized: string;
  errNotFound: string;
  errGeneric: string;
}

type RowStatus =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved' }
  | { kind: 'error'; message: string };

export function AdminPricingForm({
  models,
  defaultFx,
  labels,
}: {
  models: PricingRowModel[];
  defaultFx: string;
  labels: PricingLabels;
}) {
  const [fx, setFx] = useState(defaultFx);
  const [rows, setRows] = useState<Record<string, { input: string; output: string }>>(
    () => Object.fromEntries(models.map((m) => [m.id, { input: '', output: '' }])),
  );
  const [status, setStatus] = useState<Record<string, RowStatus>>(
    () => Object.fromEntries(models.map((m) => [m.id, { kind: 'idle' } as RowStatus])),
  );

  const fxValid = isPositiveDecimal(fx);

  function setRow(id: string, patch: Partial<{ input: string; output: string }>) {
    setRows((prev) => {
      const cur = prev[id] ?? { input: '', output: '' };
      return { ...prev, [id]: { ...cur, ...patch } };
    });
    setStatus((prev) => ({ ...prev, [id]: { kind: 'idle' } }));
  }

  async function save(id: string) {
    const row = rows[id] ?? { input: '', output: '' };
    if (!fxValid || !isPositiveDecimal(row.input) || !isPositiveDecimal(row.output)) {
      setStatus((prev) => ({
        ...prev,
        [id]: { kind: 'error', message: labels.errInvalid },
      }));
      return;
    }
    setStatus((prev) => ({ ...prev, [id]: { kind: 'saving' } }));
    const res = await setModelPrice({
      modelId: id,
      inputCnyPerMillion: row.input,
      outputCnyPerMillion: row.output,
      fxUsdCny: fx,
    });
    if (res.kind === 'ok') {
      setStatus((prev) => ({ ...prev, [id]: { kind: 'saved' } }));
    } else {
      const message =
        res.kind === 'forbidden'
          ? labels.errForbidden
          : res.kind === 'unauthorized'
            ? labels.errUnauthorized
            : res.kind === 'not_found'
              ? labels.errNotFound
              : res.kind === 'validation'
                ? res.message
                : labels.errGeneric;
      setStatus((prev) => ({ ...prev, [id]: { kind: 'error', message } }));
    }
  }

  return (
    <div className="space-y-5">
      {/* 汇率:所有行共用。填错这个所有价格都会偏,单独醒目一栏。 */}
      <Panel>
        <label className={labelCls} htmlFor="fx">
          {labels.fxLabel}
        </label>
        <input
          id="fx"
          inputMode="decimal"
          value={fx}
          onChange={(e) => setFx(e.target.value)}
          className={`${fieldCls} tabular w-40 ${fxValid ? '' : 'border-crimson'}`}
          placeholder="7.2"
        />
        <p className="mt-2 text-label text-ink-muted">{labels.fxHelp}</p>
      </Panel>

      {/* 密集定价行:单面板 + 1px 分隔线(calm-dense,同 ModelsCatalog),零阴影。 */}
      <Panel padded={false}>
        <ul className="divide-y divide-line">
          {models.map((m) => {
            const st: RowStatus = status[m.id] ?? { kind: 'idle' };
            const row = rows[m.id] ?? { input: '', output: '' };
            return (
              <li
                key={m.id}
                className="px-5 py-3.5 transition-colors duration-state ease-he hover:bg-paper"
              >
                <div className="flex flex-wrap items-end gap-x-6 gap-y-2">
                  <div className="min-w-[180px] flex-1">
                    {/* 模型 id 是技术标识 → 等宽;现价参考纯字符串展示(不经 Number) */}
                    <div className="truncate font-mono text-small font-medium text-ink">
                      {m.id}
                    </div>
                    <div className="mt-0.5 text-label text-ink-muted">{m.ownedBy}</div>
                    {(m.currentInputUsd || m.currentOutputUsd) && (
                      <p className="tabular mt-0.5 text-label text-ink-muted">
                        {[m.currentInputUsd, m.currentOutputUsd]
                          .filter(Boolean)
                          .map((v) => `$${v}`)
                          .join(' · ')}
                        {' /1K'}
                      </p>
                    )}
                  </div>
                  <div>
                    <label className={labelCls} htmlFor={`in-${m.id}`}>
                      {labels.inputLabel}
                    </label>
                    <input
                      id={`in-${m.id}`}
                      inputMode="decimal"
                      value={row.input}
                      onChange={(e) => setRow(m.id, { input: e.target.value })}
                      className={`${fieldCls} tabular w-32`}
                      placeholder={labels.inputPlaceholder}
                    />
                  </div>
                  <div>
                    <label className={labelCls} htmlFor={`out-${m.id}`}>
                      {labels.outputLabel}
                    </label>
                    <input
                      id={`out-${m.id}`}
                      inputMode="decimal"
                      value={row.output}
                      onChange={(e) => setRow(m.id, { output: e.target.value })}
                      className={`${fieldCls} tabular w-32`}
                      placeholder={labels.outputPlaceholder}
                    />
                  </div>
                  {/* 本页唯一朱砂主操作(逐行保存,与既有提交逻辑一致) */}
                  <Button
                    variant="primary"
                    onClick={() => save(m.id)}
                    disabled={st.kind === 'saving'}
                  >
                    {st.kind === 'saving' ? labels.saving : labels.save}
                  </Button>
                </div>
                {st.kind === 'saved' && (
                  <p className="mt-2 text-label text-jade">{labels.saved}</p>
                )}
                {st.kind === 'error' && (
                  <p className="mt-2 text-label text-crimson">{st.message}</p>
                )}
              </li>
            );
          })}
        </ul>
      </Panel>
    </div>
  );
}
