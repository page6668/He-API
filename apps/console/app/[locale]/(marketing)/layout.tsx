/**
 * Story 4.7 T3.1 — (marketing) route group layout.
 *
 * FIRST sibling of (auth) + (console) under `apps/console/app/[locale]/`.
 * Per `docs/architecture/source-tree.md §6` (pre-flagged), this group
 * holds anonymous-reachable marketing routes.
 *
 * Auth middleware does NOT run on these paths — `middleware.ts`
 * (existing next-intl locale negotiation) is the only middleware in the
 * chain. BR-2.9 is enforced via the (marketing) group convention; if a
 * future auth middleware is added, it MUST scope to (console) paths.
 *
 * 视觉(knowledge/taste/design-system.md):
 *  - 删掉了本组自带的第二条导航栏 —— 根 [locale]/layout 已有品牌印记 + 全站导航
 *    (含 Models),两条栏叠在一起是明显的设计冗余。
 *  - main 不再自带 padding/max-width:限宽由页面各自的 <PageShell> 统一负责,
 *    否则会双层限宽、左右内边距叠加(约 48px)。
 */
import type { ReactNode } from 'react';
import { MarketingFooter } from '@/components/marketing/MarketingFooter';

interface MarketingLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default function MarketingLayout({ children }: MarketingLayoutProps) {
  return (
    <div className="flex min-h-screen flex-col bg-paper">
      <div id="main" className="flex-1">
        {children}
      </div>
      <MarketingFooter />
    </div>
  );
}
