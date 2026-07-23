'use server';

// AD-003 —— 管理员定价后台的 BFF Server Actions(specs/admin-pricing-arch.md)。
//
// 直连网关的 admin-gated 接口,转发 he_access cookie。鉴权全在网关(RequireJWT +
// AdminGuard):非管理员会拿到 403,这里原样映射为 { kind: 'forbidden' }。
// user_id 由网关从 JWT 派生,绝不放 body(IDOR 纪律,沿用 update-my-routing-strategy)。
//
// 金额一律以字符串传递(元/百万 tokens);换算成 USD 在网关侧 SQL 内完成,
// 前端不做任何浮点运算。

import { cookies } from 'next/headers';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export type SetPriceResult =
  | { kind: 'ok' }
  | { kind: 'forbidden' } // 非管理员
  | { kind: 'unauthorized' } // 未登录
  | { kind: 'not_found' } // model_id 不是 active 模型
  | { kind: 'validation'; message: string }
  | { kind: 'error' };

export interface SetPriceInput {
  modelId: string;
  inputCnyPerMillion: string;
  outputCnyPerMillion: string;
  fxUsdCny: string;
}

export async function setModelPrice(input: SetPriceInput): Promise<SetPriceResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/models/pricing`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      // 原始「元/百万」字符串直传;网关换算 + 校验。
      body: JSON.stringify({
        model_id: input.modelId,
        input_cny_per_million: input.inputCnyPerMillion,
        output_cny_per_million: input.outputCnyPerMillion,
        fx_usd_cny: input.fxUsdCny,
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

export type DefaultsResult =
  | { kind: 'ok'; fxUsdCny: string }
  | { kind: 'forbidden' }
  | { kind: 'unauthorized' }
  | { kind: 'error' };

// 读默认汇率;顺带充当「当前用户是不是管理员」的探针 —— 403 即非管理员。
export async function getPricingDefaults(): Promise<DefaultsResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/admin/models/pricing/defaults`, {
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
    const body = (await res.json()) as { fx_usd_cny?: string };
    return { kind: 'ok', fxUsdCny: body.fx_usd_cny ?? '' };
  } catch {
    return { kind: 'error' };
  }
}
