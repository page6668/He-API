/**
 * Story 10.6 — console-side model catalogue + pricing SoT.
 *
 * The Go `packages/models-catalogue` is the gateway's capability SoT but carries
 * NO token pricing, and the console cannot import a Go package. This module is the
 * SINGLE console-side source of truth for He-model unit prices, consumed by BOTH:
 *   - the Playground cost ESTIMATE (BR-10.6.5 — pricing × usage, labeled 估算/
 *     estimated, NEVER the billed amount), and
 *   - the public Benchmark page's He-model price column (BR-10.6.11 — He prices
 *     render FROM here, never hardcoded in the curated benchmark seed).
 *
 * Money discipline (cost-SoT, [[project_cost_source_oq5_usage_ledger]]): these are
 * DISPLAY estimates from published list prices. They are never presented as a
 * billed amount — `usage_ledger` (server-side) remains the sole money SoT. The
 * gateway emits no per-request USD (X-He-Cost-Usd ABSENT), so the console never
 * reads it.
 *
 * Provenance: curated public list prices (USD per 1,000,000 tokens), last reviewed
 * 2026-06-16. Model ids mirror `packages/models-catalogue` registry.go.
 */

export interface ModelPricing {
  /** USD per 1,000,000 prompt (input) tokens. */
  inputPerMTokens: number;
  /** USD per 1,000,000 completion (output) tokens. */
  outputPerMTokens: number;
}

export interface CatalogueModel {
  /** Catalogue model id (== gateway /public/models id). */
  id: string;
  /** i18n vendor key (benchmark/models namespace `vendor.*`). */
  vendorSlug: string;
  /** Human display vendor name (fallback when no translation). */
  vendor: string;
  pricing: ModelPricing;
}

/**
 * He-model pricing SoT. The 6 headline vendors (FR-11.3): Alibaba (Qwen),
 * DeepSeek, Moonshot (Kimi), Zhipu (GLM), ByteDance (Doubao), Baidu (ERNIE),
 * plus the additional chat ids advertised by the gateway catalogue.
 */
export const HE_MODELS: readonly CatalogueModel[] = [
  { id: 'qwen-max', vendorSlug: 'alibaba', vendor: 'Alibaba', pricing: { inputPerMTokens: 1.6, outputPerMTokens: 6.4 } },
  { id: 'qwen-plus', vendorSlug: 'alibaba', vendor: 'Alibaba', pricing: { inputPerMTokens: 0.4, outputPerMTokens: 1.2 } },
  { id: 'deepseek-v3', vendorSlug: 'deepseek', vendor: 'DeepSeek', pricing: { inputPerMTokens: 0.27, outputPerMTokens: 1.1 } },
  { id: 'moonshot-v1-128k', vendorSlug: 'moonshot', vendor: 'Moonshot', pricing: { inputPerMTokens: 8.4, outputPerMTokens: 8.4 } },
  { id: 'glm-4', vendorSlug: 'zhipu', vendor: 'Zhipu', pricing: { inputPerMTokens: 1.4, outputPerMTokens: 1.4 } },
  { id: 'doubao-pro', vendorSlug: 'bytedance', vendor: 'ByteDance', pricing: { inputPerMTokens: 0.11, outputPerMTokens: 0.28 } },
  { id: 'doubao-lite', vendorSlug: 'bytedance', vendor: 'ByteDance', pricing: { inputPerMTokens: 0.04, outputPerMTokens: 0.08 } },
  { id: 'ernie-4.0', vendorSlug: 'baidu', vendor: 'Baidu', pricing: { inputPerMTokens: 4.2, outputPerMTokens: 12.6 } },
] as const;

const PRICING_BY_ID: ReadonlyMap<string, ModelPricing> = new Map(
  HE_MODELS.map((m) => [m.id, m.pricing]),
);

/** The set of known He catalogue model ids (deep-link `model ∈ catalogue` gate). */
export const CATALOGUE_MODEL_IDS: ReadonlySet<string> = new Set(HE_MODELS.map((m) => m.id));

/** True when `id` is a known He catalogue model. */
export function isCatalogueModel(id: string): boolean {
  return CATALOGUE_MODEL_IDS.has(id);
}

/** Pricing for a He model id, or null when unknown (e.g. a non-He benchmark model). */
export function pricingFor(id: string): ModelPricing | null {
  return PRICING_BY_ID.get(id) ?? null;
}
