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

  return (
    <section>
      <div className="mb-6">
        <h1 className="text-3xl font-bold text-slate-900">Models</h1>
        <p className="mt-1 text-slate-600">
          {data.data.length > 0
            ? `Browse the ${data.data.length} models available through one unified API.`
            : "Browse the models available through one unified API."}
        </p>
      </div>

      {data.data.length === 0 ? (
        <div
          role="alert"
          data-testid="models-fallback-banner"
          className="border border-amber-300 bg-amber-50 px-4 py-3 rounded text-amber-900"
        >
          Capability data temporarily unavailable; check back shortly.
        </div>
      ) : (
        <ModelsCatalog models={data.data} />
      )}
    </section>
  );
}
