import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';
import { isLocale } from '@/i18n/config';
import { notFound } from 'next/navigation';

import { Panel } from '@/components/ui/kit';

interface PageProps {
  params: { locale: string };
}

/**
 * 站点入口页(点品牌标记回到这里)。
 *
 * 曾经是 Story 2.1 的 demo 占位页(标题 + 当前语言 + 一行假数字 "19 keys"),而导航里
 * 的「Console」还指向它 —— 用户点进来看到的就是"空白"。现在导航已改指真正的
 * /dashboard,这里改为入口页:一句话说清 He-API 是什么 + 通往真实页面的入口。
 *
 * 刻意不套 <PageShell>:2.1-UNIT-086 断言本文件存在字面量 <h1>{t('demo.title')}</h1>,
 * 故手写与 PageShell 同构的外壳。入口卡文案沿用导航的英文标签(与 Models/Benchmark
 * 等公开页当前状态一致),不新增 i18n key —— 避免 10 个语言缺键导致 MISSING_MESSAGE。
 */
export default async function Page({ params: { locale } }: PageProps) {
  if (!isLocale(locale)) {
    notFound();
  }
  unstable_setRequestLocale(locale);
  const t = await getTranslations('common');

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
    <section className="mx-auto max-w-prose-page px-6 py-16 lg:px-8">
      <header className="max-w-2xl">
        <h1 className="text-display">{t('demo.title')}</h1>
        {/* 数字代替形容词(brand.md tone_rules) */}
        <p className="mt-3 text-body text-ink-secondary">
          One API for Qwen, DeepSeek, Doubao, ERNIE, GLM and Kimi — with usage,
          keys and logs in one place.
        </p>
      </header>

      <div className="mt-10 grid gap-4 sm:grid-cols-3">
        {entries.map((e) => (
          <a key={e.href} href={e.href} className="group block">
            <Panel className="h-full transition-colors duration-state ease-he group-hover:border-line-strong">
              <p className="text-h3 text-ink">{e.label}</p>
              <p className="mt-1.5 text-small text-ink-secondary">{e.desc}</p>
            </Panel>
          </a>
        ))}
      </div>
    </section>
  );
}
