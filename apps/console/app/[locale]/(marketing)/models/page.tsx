/**
 * Story 4.7 T3.2 + T3.4 — `/{locale}/models` Server Component.
 *
 * Server-side fetches /public/models (via `fetchPublicModels` — Zod-validated,
 * never throws — failures collapse to an empty matrix + fallback banner).
 *
 * generateMetadata populates Story-4.7 BR-2.5 SEO metadata: title +
 * description + og:* + canonical. Locale-aware via getTranslations.
 */
import type { Metadata } from "next";
import { getTranslations, unstable_setRequestLocale } from "next-intl/server";

import { ModelsCatalog } from "@/components/business/ModelsCatalog";
import { Notice, PageShell } from "@/components/ui/kit";
import { fetchPublicModels } from "@/lib/api/public-models";

interface PageProps {
  params: { locale: string };
}

export async function generateMetadata({ params: { locale } }: PageProps): Promise<Metadata> {
  unstable_setRequestLocale(locale);
  const t = await getTranslations({ locale, namespace: "models" });
  const title = t("page.title");
  const description = t("page.description");
  const canonical = `https://he-api.com/${locale}/models`;
  return {
    title,
    description,
    alternates: { canonical },
    openGraph: {
      title,
      description,
      url: canonical,
      type: "website",
    },
  };
}

export default async function ModelsPage({ params: { locale } }: PageProps) {
  unstable_setRequestLocale(locale);
  const data = await fetchPublicModels();

  // 公开页恒限宽居中(prose-page = 1120px)—— 禁止内容裸贴视口(design-system layout.container)
  return (
    <PageShell
      width="prose-page"
      title="Models"
      subtitle={
        data.data.length > 0
          ? `Browse the ${data.data.length} models available through one unified API.`
          : "Browse the models available through one unified API."
      }
    >
      {data.data.length === 0 ? (
        // 安静的行内提示条,不是满宽色底 banner
        <Notice tone="warning" role="alert">
          <span data-testid="models-fallback-banner">
            Capability data temporarily unavailable; check back shortly.
          </span>
        </Notice>
      ) : (
        <ModelsCatalog models={data.data} />
      )}
    </PageShell>
  );
}
