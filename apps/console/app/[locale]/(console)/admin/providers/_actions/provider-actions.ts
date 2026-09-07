'use server';

// AD-004 —— LLM Provider 配置管理 BFF Server Actions。
//
// 直连网关 admin-gated 接口(GET/PUT /v1/admin/providers)，转发 he_access cookie。
// 鉴权全在网关(RequireJWT + AdminGuard)：非管理员拿 403，这里映射为 { kind: 'forbidden' }。
//
// 与 pricing-actions 同模式：user_id 由网关从 JWT 派生，绝不在 body 传（IDAR 纪律）。
// API Key 明文绝不经过前端——List 返回 masked_key，Update 时前端传新 key 覆盖。

import { cookies } from 'next/headers';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

function gatewayURL(): string {
  // Try HE_API_GATEWAY_URL first (set via systemd EnvironmentFile on ECS).
  // Fall back to NEXT_PUBLIC_API_GATEWAY_URL (build-time .env.local, always https).
  return (
    process.env.HE_API_GATEWAY_URL ??
    process.env.NEXT_PUBLIC_API_GATEWAY_URL ??
    'http://api-gateway:8080'
  );
}

// ── Types ──

export interface ProviderItem {
  provider_name: string;
  base_url: string;
  enabled: boolean;
  masked_key: string;
  has_key: boolean;
  updated_at?: string | null;
  last_verified_at?: string | null;
  last_error?: string | null;
}

export type ListProvidersResult =
  | { kind: 'ok'; providers: ProviderItem[] }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'error' };

export async function listProviders(): Promise<ListProvidersResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/providers`, {
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
    const body = (await res.json()) as { data?: ProviderItem[] };
    return { kind: 'ok', providers: body.data ?? [] };
  } catch {
    return { kind: 'error' };
  }
}

// ── Update ──

export interface UpdateProviderInput {
  providerName: string;
  apiKey: string; // 空 = 不改 key
  baseURL: string; // 空 = 用默认
  enabled: boolean;
}

export type UpdateProviderResult =
  | { kind: 'ok' }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'not_found' }
  | { kind: 'validation'; message: string }
  | { kind: 'error' };

export async function updateProvider(input: UpdateProviderInput): Promise<UpdateProviderResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/providers/${encodeURIComponent(input.providerName)}`, {
      method: 'PUT',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      body: JSON.stringify({
        api_key: input.apiKey || undefined,
        base_url: input.baseURL || undefined,
        enabled: input.enabled,
      }),
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'forbidden' };
  if (res.status === 404) return { kind: 'not_found' };
  if (res.status === 400) {
    let message = '输入无效';
    try {
      const body = (await res.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch {
      /* 保底文案 */
    }
    return { kind: 'validation', message };
  }
  if (!res.ok) return { kind: 'error' };
  return { kind: 'ok' };
}
