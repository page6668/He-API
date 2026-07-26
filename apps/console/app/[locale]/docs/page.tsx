import type { Metadata } from 'next';
import { unstable_setRequestLocale } from 'next-intl/server';
import { notFound } from 'next/navigation';

import { MarketingFooter } from '@/components/marketing/MarketingFooter';
import { isLocale } from '@/i18n/config';
import { DocsContent } from './DocsContent';

interface PageProps {
  params: { locale: string };
}

/**
 * API 文档(快速版,项目负责人 2026-07-12 选定 A 档)。
 *
 * 内容**全部取自真实代码**,不得杜撰:
 *   - 路由:apps/api-gateway/cmd/server/main.go 的 mux.Handle 注册表
 *   - 鉴权:internal/middleware/bearer_auth.go(Authorization: Bearer <key>)
 *   - 错误码:internal/openaierr/codes.go
 *   - 代码示例:与 Playground 的 lib/playground/export-snippets.ts 同源同 BASE_URL
 *
 * 双语:页内按 locale 切换中/英,**刻意不新增 i18n 命名空间** —— 新增命名空间需在
 * 全部 10 个语言建文件(namespaces.ts 与 messages/en/*.json 有 parity 断言),
 * 任一语言缺键会 MISSING_MESSAGE 导致页面 500(本项目已踩过这个坑)。
 * 非中文 locale 显示英文,与 Models/Benchmark 等公开页当前状态一致。
 */
export function generateMetadata({ params: { locale } }: PageProps): Metadata {
  const zh = locale.startsWith('zh');
  const title = zh ? 'API 文档 — He-API' : 'API Reference — He-API';
  const description = zh
    ? '通过一个 OpenAI 兼容接口调用通义千问、DeepSeek、豆包、文心、GLM 与 Kimi。'
    : 'Call Qwen, DeepSeek, Doubao, ERNIE, GLM and Kimi through one OpenAI-compatible API.';
  // canonical + og 照抄 (marketing)/benchmark/page.tsx 的模式
  const canonical = `https://he-api.com/${locale}/docs`;
  return {
    title,
    description,
    alternates: { canonical },
    openGraph: { title, description, url: canonical, type: 'website' },
  };
}

export default function DocsPage({ params: { locale } }: PageProps) {
  if (!isLocale(locale)) {
    notFound();
  }
  unstable_setRequestLocale(locale);
  // docs 不在 (marketing) 路由组内,页尾骨架与 (marketing)/layout.tsx 保持一致
  return (
    <div className="flex min-h-screen flex-col bg-paper">
      <div className="flex-1">
        <DocsContent locale={locale} />
      </div>
      <MarketingFooter />
    </div>
  );
}
