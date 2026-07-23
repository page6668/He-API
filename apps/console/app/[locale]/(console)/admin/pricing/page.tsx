/**
 * AD-003 —— 管理员定价后台页(specs/admin-pricing-arch.md)。
 *
 * Server Component。鉴权是网关的职责(RequireJWT + AdminGuard):本页调
 * getPricingDefaults() 既取默认汇率、又充当「是不是管理员」的探针 ——
 *   401 → 跳登录;403 → 显示「需要管理员权限」(非管理员无法看到表单);
 *   ok  → 渲染定价表单。
 *
 * 不新建 i18n 命名空间(会要求 10 个语言文件,缺一即 500)——这是给运营者用的
 * 内部工具,内联中文,与 docs 页同策略。
 */
import type { Metadata } from 'next';
import { redirect } from 'next/navigation';
import { unstable_setRequestLocale } from 'next-intl/server';

import { Notice, PageShell } from '@/components/ui/kit';
import { AdminPricingForm, type PricingRowModel } from '@/components/business/AdminPricingForm';
import { fetchPublicModels } from '@/lib/api/public-models';
import {
  getPricingDefaults,
  type DefaultsResult,
} from './_actions/pricing-actions';

export const metadata: Metadata = {
  title: '模型定价 · 管理',
  robots: { index: false, follow: false }, // 后台页不进搜索引擎
};

interface PageProps {
  params: { locale: string };
}

export default async function AdminPricingPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);

  const defaults: DefaultsResult = await getPricingDefaults();

  if (defaults.kind === 'unauthorized') {
    redirect(`/${locale}/signin?next=/${locale}/admin/pricing`);
  }

  if (defaults.kind === 'forbidden') {
    return (
      <PageShell title="模型定价">
        <Notice tone="error" role="alert">
          需要管理员权限。当前账号不是管理员,如需改价请联系平台负责人为你的账号授予 admin 角色。
        </Notice>
      </PageShell>
    );
  }

  if (defaults.kind === 'error') {
    return (
      <PageShell title="模型定价">
        <Notice tone="error" role="alert">
          无法连接后台服务,请稍后重试。
        </Notice>
      </PageShell>
    );
  }

  // 取当前 active 模型列表(公开清单,已带当前价供参考)。
  const catalogue = await fetchPublicModels();
  const models: PricingRowModel[] = catalogue.data.map((m) => ({
    id: m.id,
    ownedBy: m.owned_by,
    currentInputUsd: m.pricing?.input_per_1k_tokens,
    currentOutputUsd: m.pricing?.output_per_1k_tokens,
  }));

  return (
    <PageShell
      title="模型定价"
      subtitle="填写各模型的上游成本价(元/百万 tokens)。保存后换算成美元入库,约 5 分钟内在模型广场生效。加价率由系统统一处理,这里填成本价。"
    >
      {models.length === 0 ? (
        <Notice tone="warning" role="status">
          当前没有可定价的上架模型。
        </Notice>
      ) : (
        <AdminPricingForm models={models} defaultFx={defaults.fxUsdCny} />
      )}
    </PageShell>
  );
}
