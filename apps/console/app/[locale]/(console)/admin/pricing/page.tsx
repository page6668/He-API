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
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { Notice, PageShell } from '@/components/ui/kit';
import {
  AdminPricingForm,
  type PricingRowModel,
  type PricingLabels,
} from '@/components/business/AdminPricingForm';
import { fetchPublicModels } from '@/lib/api/public-models';
import {
  getPricingDefaults,
  type DefaultsResult,
} from './_actions/pricing-actions';

export async function generateMetadata({
  params: { locale },
}: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'account' });
  return {
    title: t('pricing.title'),
    robots: { index: false, follow: false }, // 后台页不进搜索引擎
  };
}

interface PageProps {
  params: { locale: string };
}

export default async function AdminPricingPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'account' });

  const defaults: DefaultsResult = await getPricingDefaults();

  if (defaults.kind === 'unauthorized') {
    redirect(`/${locale}/signin?next=/${locale}/admin/pricing`);
  }

  if (defaults.kind === 'forbidden') {
    return (
      <PageShell title={t('pricing.title')}>
        <Notice tone="error" role="alert">
          {t('pricing.forbidden')}
        </Notice>
      </PageShell>
    );
  }

  if (defaults.kind === 'error') {
    return (
      <PageShell title={t('pricing.title')}>
        <Notice tone="error" role="alert">
          {t('pricing.connectError')}
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

  // 表单是 client 组件,文案经 props 传入(单一来源,避免客户端再取一次命名空间)。
  const labels: PricingLabels = {
    fxLabel: t('pricing.fxLabel'),
    fxHelp: t('pricing.fxHelp'),
    inputLabel: t('pricing.inputLabel'),
    outputLabel: t('pricing.outputLabel'),
    inputPlaceholder: t('pricing.inputPlaceholder'),
    outputPlaceholder: t('pricing.outputPlaceholder'),
    save: t('pricing.save'),
    saving: t('pricing.saving'),
    saved: t('pricing.saved'),
    errInvalid: t('pricing.errInvalid'),
    errForbidden: t('pricing.errForbidden'),
    errUnauthorized: t('pricing.errUnauthorized'),
    errNotFound: t('pricing.errNotFound'),
    errGeneric: t('pricing.errGeneric'),
  };

  return (
    <PageShell title={t('pricing.title')} subtitle={t('pricing.subtitle')}>
      {models.length === 0 ? (
        <Notice tone="warning" role="status">
          {t('pricing.empty')}
        </Notice>
      ) : (
        <AdminPricingForm models={models} defaultFx={defaults.fxUsdCny} labels={labels} />
      )}
    </PageShell>
  );
}
