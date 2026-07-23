'use server';

// AD-003 —— 查当前用户角色(specs/admin-pricing-arch.md)。
//
// 供 (console) layout 决定是否显示「模型定价」管理入口。BFF:转发 he_access
// cookie 到网关 GET /v1/me/role。
//
// fail-safe:任何异常(未登录、网络错、非 2xx)都返回 'user' —— 不确定时按普通
// 用户处理,入口隐藏。真正的写钱拦截在网关 AdminGuard,这里只影响 UI 显隐。

import { cookies } from 'next/headers';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export type UserRole = 'admin' | 'user';

export async function getMyRole(): Promise<UserRole> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return 'user';

  try {
    const res = await fetch(`${gatewayURL()}/v1/me/role`, {
      method: 'GET',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
      },
      cache: 'no-store',
    });
    if (!res.ok) return 'user';
    const body = (await res.json()) as { role?: string };
    return body.role === 'admin' ? 'admin' : 'user';
  } catch {
    return 'user';
  }
}
