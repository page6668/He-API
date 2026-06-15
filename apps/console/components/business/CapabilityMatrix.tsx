/**
 * Story 4.7 T4.2 — `CapabilityMatrix`.
 *
 * Renders the 11 He-API models as a responsive table:
 *
 *   - ≥ 768 px (Tailwind `md:`) — semantic `<table>` with caption + scoped
 *     headers.
 *   - < 768 px — per-model `<article role="region">` cards (one per row).
 *
 * Tailwind class pairings:
 *   - desktop: `hidden md:table` on the <table>
 *   - mobile:  `md:hidden` on the card stack
 *
 * The component is a pure presentation Server Component — it receives the
 * `models` array from the page and renders without side effects so it can
 * be exercised under Vitest with React Testing Library.
 */
import type { JSX } from "react";
import { useTranslations, useLocale } from "next-intl";

import type { ModelEntry } from "@/lib/api/public-models";

import { CapabilityBadge } from "./CapabilityBadge";

interface CapabilityMatrixProps {
  models: ModelEntry[];
}

// Vendor slug map — keys match `owned_by` values from the gateway; values
// route to the i18n vendor.* namespace. Story-4.7 vendor.* keyset is
// committed by T0.5 / T0.7.
const VENDOR_SLUGS = ["alibaba", "deepseek", "moonshot", "zhipu", "bytedance", "baidu", "he-api"] as const;
type VendorSlug = (typeof VENDOR_SLUGS)[number];

function vendorKey(ownedBy: string): VendorSlug | "he-api" {
  if ((VENDOR_SLUGS as readonly string[]).includes(ownedBy)) {
    return ownedBy as VendorSlug;
  }
  // Fallback — unrecognised owned_by collapses to he-api so the i18n
  // lookup never fails at runtime; capability data drift surfaces in the
  // /public/models shape-drift slog instead.
  return "he-api";
}

export function CapabilityMatrix({ models }: CapabilityMatrixProps): JSX.Element {
  const t = useTranslations("models");
  const locale = useLocale();
  const numberFormat = new Intl.NumberFormat(locale);

  const yesLabel = t("capability.value.yes");
  const noLabel = t("capability.value.no");

  return (
    <>
      {/* Desktop matrix table — md: breakpoint inclusive. */}
      <table className="hidden md:table w-full border-collapse text-sm">
        <caption className="text-start text-slate-600 mb-2">
          {t("table.caption")}
        </caption>
        <thead>
          <tr className="border-b">
            <th scope="col" className="text-start p-2">{t("table.column.model")}</th>
            <th scope="col" className="text-start p-2">{t("table.column.vendor")}</th>
            <th scope="col" className="p-2">{t("table.column.chat")}</th>
            <th scope="col" className="p-2">{t("table.column.streaming")}</th>
            <th scope="col" className="p-2">{t("table.column.functionCalling")}</th>
            <th scope="col" className="p-2">{t("table.column.vision")}</th>
            <th scope="col" className="p-2">{t("table.column.jsonMode")}</th>
            <th scope="col" className="p-2 text-end">{t("table.column.contextWindow")}</th>
            <th scope="col" className="p-2 text-end">{t("table.column.maxOutput")}</th>
          </tr>
        </thead>
        <tbody>
          {models.map((m) => (
            <tr key={m.id} className="border-b">
              <th scope="row" className="text-start p-2 font-mono">{m.id}</th>
              <td className="p-2">{t(`vendor.${vendorKey(m.owned_by)}` as never)}</td>
              <td className="p-2 text-center">
                <CapabilityBadge present={m.capabilities.chat} label={m.capabilities.chat ? yesLabel : noLabel} />
              </td>
              <td className="p-2 text-center">
                <CapabilityBadge present={m.capabilities.streaming} label={m.capabilities.streaming ? yesLabel : noLabel} />
              </td>
              <td className="p-2 text-center">
                <CapabilityBadge present={m.capabilities.function_calling} label={m.capabilities.function_calling ? yesLabel : noLabel} />
              </td>
              <td className="p-2 text-center">
                <CapabilityBadge present={m.capabilities.vision} label={m.capabilities.vision ? yesLabel : noLabel} />
              </td>
              <td className="p-2 text-center">
                <CapabilityBadge present={m.capabilities.json_mode} label={m.capabilities.json_mode ? yesLabel : noLabel} />
              </td>
              <td className="p-2 text-end tabular-nums">
                {numberFormat.format(m.capabilities.context_window_tokens)}
              </td>
              <td className="p-2 text-end tabular-nums">
                {numberFormat.format(m.capabilities.max_output_tokens)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {/* Mobile card stack — viewports < 768 px. */}
      <div className="md:hidden flex flex-col gap-4">
        {models.map((m) => (
          <article
            key={m.id}
            role="region"
            aria-label={m.id}
            className="border rounded p-4"
          >
            <h2 className="font-mono text-base mb-1">{m.id}</h2>
            <p className="text-sm text-slate-600 mb-3">
              {t(`vendor.${vendorKey(m.owned_by)}` as never)}
            </p>
            <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
              <dt>{t("capability.chat")}</dt>
              <dd>
                <CapabilityBadge present={m.capabilities.chat} label={m.capabilities.chat ? yesLabel : noLabel} />
              </dd>
              <dt>{t("capability.streaming")}</dt>
              <dd>
                <CapabilityBadge present={m.capabilities.streaming} label={m.capabilities.streaming ? yesLabel : noLabel} />
              </dd>
              <dt>{t("capability.functionCalling")}</dt>
              <dd>
                <CapabilityBadge present={m.capabilities.function_calling} label={m.capabilities.function_calling ? yesLabel : noLabel} />
              </dd>
              <dt>{t("capability.vision")}</dt>
              <dd>
                <CapabilityBadge present={m.capabilities.vision} label={m.capabilities.vision ? yesLabel : noLabel} />
              </dd>
              <dt>{t("capability.jsonMode")}</dt>
              <dd>
                <CapabilityBadge present={m.capabilities.json_mode} label={m.capabilities.json_mode ? yesLabel : noLabel} />
              </dd>
              <dt>{t("capability.contextWindow")}</dt>
              <dd className="tabular-nums">{numberFormat.format(m.capabilities.context_window_tokens)}</dd>
              <dt>{t("capability.maxOutput")}</dt>
              <dd className="tabular-nums">{numberFormat.format(m.capabilities.max_output_tokens)}</dd>
            </dl>
          </article>
        ))}
      </div>
    </>
  );
}
