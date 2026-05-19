/**
 * Story 4.7 T3.1 — (marketing) route group layout.
 *
 * FIRST sibling of (auth) + (console) under `apps/console/app/[locale]/`.
 * Per `docs/architecture/source-tree.md §6` (pre-flagged), this group
 * holds anonymous-reachable marketing routes. Story 4.7 lands ONLY the
 * /models route + this layout stub. Epic-10 marketing-launch Stories
 * expand the layout (Pricing / Benchmark / etc.) and inherit the
 * anonymous-reachable pattern this Story establishes.
 *
 * Auth middleware does NOT run on these paths — `middleware.ts`
 * (existing next-intl locale negotiation) is the only middleware in the
 * chain. BR-2.9 is enforced via the (marketing) group convention; if a
 * future auth middleware is added, it MUST scope to (console) paths.
 */
import type { ReactNode } from "react";
import { useTranslations } from "next-intl";

interface MarketingLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default function MarketingLayout({
  children,
  params: { locale },
}: MarketingLayoutProps) {
  return (
    <div className="min-h-screen flex flex-col">
      <MarketingHeader locale={locale} />
      <main id="main" className="flex-1 px-6 py-8 max-w-screen-xl mx-auto w-full">
        {children}
      </main>
      <MarketingFooter />
    </div>
  );
}

function MarketingHeader({ locale }: { locale: string }) {
  const t = useTranslations("models");
  return (
    <header className="border-b px-6 py-3">
      <nav className="flex items-center justify-between max-w-screen-xl mx-auto">
        <a href={`/${locale}`} className="font-semibold">
          {t("nav.home")}
        </a>
        <ul className="flex items-center gap-4 text-sm">
          <li>
            <a href={`/${locale}/models`} aria-current="page">
              {t("nav.models")}
            </a>
          </li>
        </ul>
      </nav>
    </header>
  );
}

function MarketingFooter() {
  return (
    <footer className="border-t px-6 py-4 text-xs text-slate-500">
      © He-API
    </footer>
  );
}
