import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';
import { isLocale } from '@/i18n/config';
import { notFound } from 'next/navigation';
import { ArrowRight } from 'lucide-react';

import { Panel, linkCls } from '@/components/ui/kit';
import { fetchPublicModels } from '@/lib/api/public-models';
import { MarketingFooter } from '@/components/marketing/MarketingFooter';

interface PageProps {
  params: { locale: string };
}

/**
 * 站点入口页(点品牌标记回到这里)—— M7-A 门面首页。
 *
 * 调性:更大的字、更多的纸、同一枚印、数字作证言(design-system.md「宣纸·墨·朱砂印」)。
 * 层级只靠 1px 暖边框与明度差,零阴影;动效仅 hover 边框/纸色一档与 focus ring。
 *
 * 刻意不套 <PageShell>:2.1-UNIT-086 断言本文件存在字面量 <h1>{t('demo.title')}</h1>,
 * 故手写与 PageShell 同构的外壳。全部新文案为英文内联(marketing surface),零新增
 * i18n key —— 避免 10 个语言缺键导致 MISSING_MESSAGE。
 *
 * 铁律2(朱砂只落一处):全页唯一 seal = hero 的「Open the playground」链接;
 * 它手写复刻 kit Button primary 的类名(链接语义须是 <a>,Button 是 <button>)。
 */

/**
 * 代码即插画 —— 与 lib/playground/export-snippets.ts 的 curlSnippet 逐字同源:
 * 端点(https://api.he-api.com/v1/chat/completions)与两个 -H 头逐字一致,不杜撰;
 * 仅 body 采用紧凑 JSON 排版,把片段收进 7 行。
 */
const CURL_LINES = [
  'curl https://api.he-api.com/v1/chat/completions \\',
  '  -H "Authorization: Bearer $HE_API_KEY" \\',
  '  -H "Content-Type: application/json" \\',
  "  -d '{",
  '    "model": "qwen-plus",',
  '    "messages": [{ "role": "user", "content": "Hello" }]',
  "  }'",
].join('\n');

/**
 * 手写 <a> 版 kit Button primary(类名与 components/ui/kit.tsx 完全一致,
 * 仅去掉对链接无意义的 disabled:* 变体)。
 */
const primaryLinkCls =
  'inline-flex items-center rounded-md px-4 py-2 text-small font-medium transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 focus-visible:ring-offset-2 bg-seal text-white hover:bg-seal-hover';

export default async function Page({ params: { locale } }: PageProps) {
  if (!isLocale(locale)) {
    notFound();
  }
  unstable_setRequestLocale(locale);
  const t = await getTranslations('common');

  // 数字作证言:N 来自 /public/models(ISR 300s,never-throws)。取数失败/为空时
  // 整条退化为不含 N 的静态文案 —— 首屏禁止出现 warning banner/Notice。
  const { data: models } = await fetchPublicModels();
  const proofLine =
    models.length > 0
      ? `${models.length} models · 6 providers · 1 OpenAI-compatible API`
      : '6 providers · 1 OpenAI-compatible API';

  const entries = [
    {
      href: `/${locale}/models`,
      label: 'Models',
      desc: 'Browse every model available through one unified API.',
    },
    {
      href: `/${locale}/playground`,
      label: 'Playground',
      desc: 'Run a live completion, compare models A/B, export code.',
    },
    {
      href: `/${locale}/dashboard`,
      label: 'Console',
      desc: 'Usage, API keys, request logs and account settings.',
    },
  ];

  return (
    <>
      <section className="mx-auto max-w-prose-page px-6 pb-24 pt-20 lg:px-8 lg:pt-24">
        {/* hero:首屏留白升档,左对齐编辑式 */}
        <header className="max-w-2xl">
          <h1 className="text-display">{t('demo.title')}</h1>
          <p className="mt-4 text-body text-ink-secondary">
            One API for Qwen, DeepSeek, Doubao, ERNIE, GLM and Kimi — with usage,
            keys and logs in one place.
          </p>
          {/* 数字代替形容词(brand.md tone_rules):Mono 证言条 */}
          <p className="tabular mt-6 text-small text-ink-muted">{proofLine}</p>
          <div className="mt-8 flex flex-wrap items-center gap-6">
            {/* 全页唯一的朱砂印 */}
            <a href={`/${locale}/playground`} className={primaryLinkCls}>
              Open the playground
            </a>
            <a href={`/${locale}/docs`} className={linkCls}>
              Read the docs
            </a>
          </div>
        </header>

        {/* 代码即插画:hero 与入口区之间,surface_sunken 底 + 1px 暖边框 */}
        <div className="mt-16">
          <p className="text-label text-ink-muted">One request, any model</p>
          <pre
            dir="ltr"
            className="mt-3 overflow-x-auto rounded-lg border border-line bg-surface-sunken p-5 font-mono text-small leading-relaxed text-ink"
          >
            <code>{CURL_LINES}</code>
          </pre>
        </div>

        {/* 入口区:去卡片墙 → 单 Panel 密集列表行(对齐 ModelsCatalog 行语法) */}
        <div className="mt-16">
          <p className="text-label text-ink-muted">Start here</p>
          <Panel padded={false} className="mt-3">
            <ul className="divide-y divide-line">
              {entries.map((e) => (
                <li key={e.href}>
                  <a
                    href={e.href}
                    className="flex items-center gap-6 px-5 py-4 transition-colors duration-state ease-he hover:bg-paper focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30"
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block text-h3 text-ink">{e.label}</span>
                      <span className="mt-0.5 block text-small text-ink-secondary">
                        {e.desc}
                      </span>
                    </span>
                    <ArrowRight
                      size={16}
                      strokeWidth={1.5}
                      aria-hidden="true"
                      className="shrink-0 text-ink-muted rtl:-scale-x-100"
                    />
                  </a>
                </li>
              ))}
            </ul>
          </Panel>
        </div>
      </section>

      <MarketingFooter />
    </>
  );
}
