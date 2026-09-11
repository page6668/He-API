/**
 * AD-006 — 管理员模型管理页。
 *
 * Server Component。鉴权模式与 pricing / providers 页完全一致：
 *   401 → 跳登录；403 → 显示「需要管理员权限」；ok → 渲染模型管理表。
 *
 * 不新建 i18n 命名空间（10 语言文件缺一即 500）——内部工具，内联中文。
 */
import type { Metadata } from 'next';
import { redirect } from 'next/navigation';
import { unstable_setRequestLocale } from 'next-intl/server';

import { Notice, PageShell } from '@/components/ui/kit';
import { AdminModelsForm, type ModelRowModel, type ModelLabels } from '@/components/business/AdminModelsForm';
import { listModels, type ListModelsResult } from './_actions/models-actions';

export async function generateMetadata({
  params: { locale },
}: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  return {
    title: '模型管理',
    robots: { index: false, follow: false },
  };
}

interface PageProps {
  params: { locale: string };
}

export default async function AdminModelsPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);

  const result: ListModelsResult = await listModels();

  if (result.kind === 'unauthorized') {
    redirect(`/${locale}/signin?next=/${locale}/admin/models`);
  }

  if (result.kind === 'forbidden') {
    return (
      <PageShell title="模型管理">
        <Notice tone="error" role="alert">
          需要管理员权限。当前账号不是管理员。
        </Notice>
      </PageShell>
    );
  }

  if (result.kind === 'error') {
    return (
      <PageShell title="模型管理">
        <Notice tone="error" role="alert">
          无法连接后台服务，请稍后重试。
        </Notice>
      </PageShell>
    );
  }

  const models: ModelRowModel[] = result.models.map((m) => ({
    id: m.id,
    displayName: m.display_name,
    vendor: m.vendor,
    capabilities: m.capabilities,
    upstreamModelId: m.upstream_model_id,
    status: m.status,
    createdAt: m.created_at,
    updatedAt: m.updated_at ?? null,
  }));

  const labels: ModelLabels = {
    vendorLabel: '厂商',
    capabilitiesLabel: '能力',
    upstreamModelIdLabel: '上游模型 ID',
    statusLabel: '状态',
    statusActive: '可用',
    statusPending: '待上架',
    statusDeprecated: '已下架',
    createTitle: '新增模型',
    createId: '模型 ID',
    createIdPlaceholder: '如 qwen3.7-plus',
    createDisplayName: '显示名称',
    createDisplayNamePlaceholder: '如 Qwen3.7 Plus',
    createVendor: '厂商',
    createVendorPlaceholder: '选择厂商',
    createCapabilities: '能力',
    createUpstreamModelId: '上游模型 ID（可选）',
    createUpstreamModelIdPlaceholder: '留空则与模型 ID 相同',
    create: '添加',
    creating: '添加中…',
    deprecate: '下架',
    deprecating: '下架中…',
    deprecateConfirmTitle: '确认下架',
    deprecateConfirmBody: '确定要下架「{modelId}」吗？下架后该模型将不再对外可见。',
    cancel: '取消',
    save: '保存',
    saving: '保存中…',
    saved: '已保存',
    errConflict: '该 ID 已存在',
    errValidation: '输入无效',
    errForbidden: '需要管理员权限',
    errUnauthorized: '登录已失效，请重新登录',
    errNotFound: '模型不存在',
    errGeneric: '操作失败，请重试',
    emptyTitle: '暂无模型',
    emptyBody: '点击上方「新增模型」添加第一个模型。',
  };

  return (
    <PageShell title="模型管理" subtitle="管理模型目录的上架与下架。新增模型默认「待上架」，需定价后对外可见。">
      {models.length === 0 ? (
        <Notice tone="info" role="status">
          {labels.emptyTitle} — {labels.emptyBody}
        </Notice>
      ) : (
        <AdminModelsForm models={models} labels={labels} />
      )}
    </PageShell>
  );
}
