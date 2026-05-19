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

import { CapabilityMatrix } from "@/components/business/CapabilityMatrix";
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
  const t = await getTranslations({ locale, namespace: "models" });
  const data = await fetchPublicModels();

  return (
    <section>
      <h1 className="text-2xl font-bold mb-2">{t("page.heading")}</h1>
      <p className="text-slate-600 mb-6">{t("page.subheading")}</p>

      {data.data.length === 0 ? (
        <div
          role="alert"
          data-testid="models-fallback-banner"
          className="border border-amber-300 bg-amber-50 px-4 py-3 rounded text-amber-900"
        >
          {t("error.unavailable")}
        </div>
      ) : (
        <CapabilityMatrix models={data.data} />
      )}
    </section>
  );
}
