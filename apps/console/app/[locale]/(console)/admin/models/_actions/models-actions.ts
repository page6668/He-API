'use server';

// AD-006 — 管理员模型管理 BFF Server Actions。
//
// 直连网关 admin-gated 接口（GET/POST /v1/admin/models，
// POST /v1/admin/models/{id}/deprecate），转发 he_access cookie。
// 鉴权全在网关（RequireJWT + AdminGuard）：非管理员拿 403。

import { cookies } from 'next/headers';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

function gatewayURL(): string {
  return (
    process.env.HE_API_GATEWAY_URL ??
    process.env.NEXT_PUBLIC_API_GATEWAY_URL ??
    'http://api-gateway:8080'
  );
}

// ── Types ─────────────────────────────────────────────────────────────────────

export interface ModelItem {
  id: string;
  display_name: string;
  vendor: string;
  capabilities: Record<string, any>;
  upstream_model_id: string;
  status: 'active' | 'pending' | 'deprecated';
  created_at: string;
  updated_at?: string | null;
}

export type ListModelsResult =
  | { kind: 'ok'; models: ModelItem[] }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'error' };

// ── List ──────────────────────────────────────────────────────────────────────

export async function listModels(): Promise<ListModelsResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/models`, {
      method: 'GET',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
      },
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'forbidden' };
  if (!res.ok) return { kind: 'error' };

  try {
    const body = (await res.json()) as { data?: ModelItem[] };
    return { kind: 'ok', models: body.data ?? [] };
  } catch {
    return { kind: 'error' };
  }
}

// ── Create ────────────────────────────────────────────────────────────────────

export interface CreateModelInput {
  id: string;
  displayName: string;
  vendor: string;
  capabilities: Record<string, any>;
  upstreamModelId?: string;
}

export type CreateModelResult =
  | { kind: 'ok'; modelId: string }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'conflict'; message: string }
  | { kind: 'validation'; message: string }
  | { kind: 'error' };

export async function createModel(input: CreateModelInput): Promise<CreateModelResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/models`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      body: JSON.stringify({
        id: input.id,
        display_name: input.displayName,
        vendor: input.vendor,
        capabilities: input.capabilities,
        upstream_model_id: input.upstreamModelId ?? '',
      }),
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'forbidden' };
  if (res.status === 409) {
    let message = '该 ID 已存在';
    try {
      const body = (await res.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch { /* ok */ }
    return { kind: 'conflict', message };
  }
  if (res.status === 400) {
    let message = '输入无效';
    try {
      const body = (await res.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch { /* ok */ }
    return { kind: 'validation', message };
  }
  if (!res.ok) return { kind: 'error' };
  return { kind: 'ok', modelId: input.id };
}

// ── Deprecate ─────────────────────────────────────────────────────────────────

export type DeprecateModelResult =
  | { kind: 'ok' }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'not_found' }
  | { kind: 'error' };

export async function deprecateModel(modelId: string): Promise<DeprecateModelResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/models/${encodeURIComponent(modelId)}/deprecate`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
      },
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'forbidden' };
  if (res.status === 404) return { kind: 'not_found' };
  if (!res.ok) return { kind: 'error' };
  return { kind: 'ok' };
}
