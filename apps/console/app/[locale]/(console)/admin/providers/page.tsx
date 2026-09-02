/**
 * AD-004 —— LLM Provider 配置管理页。
 *
 * Server Component。鉴权模式与 pricing 页完全一致：
 *   401 → 跳登录；403 → 显示「需要管理员权限」；ok → 渲染 6 张 provider 卡片。
 *
 * 不新建 i18n 命名空间（10 语言文件缺一即 500）——内部工具，内联中文。
 */
import type { Metadata } from 'next';
import { redirect } from 'next/navigation';
import { getTranslations, unstable_setRequestLocale } from 'next-intl/server';

import { Notice, PageShell } from '@/components/ui/kit';
import { AdminProvidersForm, type ProviderRowModel, type ProviderLabels } from '@/components/business/AdminProvidersForm';
import { listProviders, type ListProvidersResult } from './_actions/provider-actions';

export async function generateMetadata({
  params: { locale },
}: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: 'account' });
  return {
    title: 'LLM Provider 配置',
    robots: { index: false, follow: false },
  };
}

interface PageProps {
  params: { locale: string };
}

export default async function AdminProvidersPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);

  const result: ListProvidersResult = await listProviders();

  if (result.kind === 'unauthorized') {
    redirect(`/${locale}/signin?next=/${locale}/admin/providers`);
  }

  if (result.kind === 'forbidden') {
    return (
      <PageShell title="LLM Provider 配置">
        <Notice tone="error" role="alert">
          需要管理员权限。当前账号不是管理员。
        </Notice>
      </PageShell>
    );
  }

  if (result.kind === 'error') {
    return (
      <PageShell title="LLM Provider 配置">
        <Notice tone="error" role="alert">
          无法连接后台服务，请稍后重试。
        </Notice>
      </PageShell>
    );
  }

  const models: ProviderRowModel[] = result.providers.map((p) => ({
    name: p.provider_name,
    baseURL: p.base_url,
    enabled: p.enabled,
    maskedKey: p.masked_key,
    hasKey: p.has_key,
    updatedAt: p.updated_at ?? null,
    verifiedAt: p.last_verified_at ?? null,
    lastError: p.last_error ?? null,
  }));

  const labels: ProviderLabels = {
    apiKeyLabel: 'API Key',
    apiKeyPlaceholder: '粘贴新的 API Key（留空则不修改）',
    baseURLLabel: 'Base URL',
    enabledLabel: '启用',
    save: '保存',
    saving: '保存中…',
    saved: '已保存',
    errInvalid: '输入无效',
    errForbidden: '需要管理员权限',
    errUnauthorized: '登录已失效，请重新登录',
    errNotFound: '该 provider 不存在',
    errGeneric: '保存失败，请重试',
    statusEnabled: '已启用',
    statusDisabled: '已停用',
    statusHasKey: '已配置',
    statusNoKey: '未配置 Key',
    lastVerified: '上次验证',
    lastError: '上次错误',
    notVerified: '未验证',
  };

  return (
    <PageShell title="LLM Provider 配置" subtitle="管理各上游模型的 API Key 与 Base URL。保存后即时生效，无需重启服务。">
      <AdminProvidersForm providers={models} labels={labels} />
    </PageShell>
  );
}
