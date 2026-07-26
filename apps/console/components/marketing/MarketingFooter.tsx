/**
 * MarketingFooter — 门面页共享页尾。
 *
 * 原内联于 `app/[locale]/(marketing)/layout.tsx`,M7 提取为共享组件:
 * 首页(`[locale]/page.tsx`)与 docs(`[locale]/docs/`)不在 (marketing) 组内
 * (首页路径被 2.1-UNIT-085/086 源码断言锁死,不可移组),但同属对外门面,
 * 需要同一个页尾收底。
 */
export function MarketingFooter() {
  return (
    <footer className="border-t border-line">
      <div className="mx-auto max-w-prose-page px-6 py-5 text-label text-ink-muted lg:px-8">
        © He-API
      </div>
    </footer>
  );
}
