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

type RowStatus =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved' }
  | { kind: 'error'; message: string };

export function AdminPricingForm({
  models,
  defaultFx,
}: {
  models: PricingRowModel[];
  defaultFx: string;
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
        [id]: { kind: 'error', message: '输入价、输出价、汇率都必须是大于 0 的数字' },
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
          ? '需要管理员权限'
          : res.kind === 'unauthorized'
            ? '登录已失效,请重新登录'
            : res.kind === 'not_found'
              ? '该模型不存在或已下架'
              : res.kind === 'validation'
                ? res.message
                : '保存失败,请重试';
      setStatus((prev) => ({ ...prev, [id]: { kind: 'error', message } }));
    }
  }

  return (
    <div className="space-y-5">
      {/* 汇率:所有行共用。填错这个所有价格都会偏,单独醒目一栏。 */}
      <Panel>
        <label className={labelCls} htmlFor="fx">
          换算汇率 USD → CNY(默认取系统最新,可改)
        </label>
        <input
          id="fx"
          inputMode="decimal"
          value={fx}
          onChange={(e) => setFx(e.target.value)}
          className={`${fieldCls} tabular mt-1 w-40 ${fxValid ? '' : 'border-crimson'}`}
          placeholder="7.2"
        />
        <p className="mt-2 text-label text-ink-muted">
          你填「元 / 百万 tokens」,系统按此汇率换算成美元存储。改价是新增记录,旧价保留可追溯。
        </p>
      </Panel>

      <div className="space-y-3">
        {models.map((m) => {
          const st: RowStatus = status[m.id] ?? { kind: 'idle' };
          const row = rows[m.id] ?? { input: '', output: '' };
          return (
            <Panel key={m.id}>
              <div className="flex flex-wrap items-end gap-4">
                <div className="min-w-[180px] flex-1">
                  <div className="font-mono text-h3 text-ink">{m.id}</div>
                  <div className="text-label text-ink-muted">{m.ownedBy}</div>
                </div>
                <div>
                  <label className={labelCls} htmlFor={`in-${m.id}`}>
                    输入价 元/百万
                  </label>
                  <input
                    id={`in-${m.id}`}
                    inputMode="decimal"
                    value={row.input}
                    onChange={(e) => setRow(m.id, { input: e.target.value })}
                    className={`${fieldCls} tabular mt-1 w-32`}
                    placeholder="如 5.76"
                  />
                </div>
                <div>
                  <label className={labelCls} htmlFor={`out-${m.id}`}>
                    输出价 元/百万
                  </label>
                  <input
                    id={`out-${m.id}`}
                    inputMode="decimal"
                    value={row.output}
                    onChange={(e) => setRow(m.id, { output: e.target.value })}
                    className={`${fieldCls} tabular mt-1 w-32`}
                    placeholder="如 14.4"
                  />
                </div>
                <Button
                  variant="primary"
                  onClick={() => save(m.id)}
                  disabled={st.kind === 'saving'}
                >
                  {st.kind === 'saving' ? '保存中…' : '保存'}
                </Button>
              </div>
              {st.kind === 'saved' && (
                <p className="mt-2 text-label text-jade">已保存,约 5 分钟内在模型广场生效。</p>
              )}
              {st.kind === 'error' && (
                <p className="mt-2 text-label text-crimson">{st.message}</p>
              )}
            </Panel>
          );
        })}
      </div>
    </div>
  );
}
