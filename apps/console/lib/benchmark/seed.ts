/**
 * Story 10.6 — committed curated Benchmark seed (OQ-10.6-2 ruling: MVP = static
 * human-curated seed; a real automated cross-vendor pipeline is OUT of scope,
 * deferred to 10.7+). NOT a DB table/service.
 *
 * PROVENANCE is mandatory (BR-10.6.9): every point carries a `source`; the seed
 * carries `last_updated` + `methodologySources`; the page renders a visible
 * disclaimer ("curated / third-party public sources, not a real-time benchmark").
 *
 * SoT discipline (BR-10.6.11): He-model points DO NOT carry a price — He prices
 * render LIVE from `lib/catalogue/pricing` (the console pricing SoT). Only the
 * non-He reference models (GPT-4 / Claude / Gemini, absent from the catalogue)
 * carry a curated `costPer1M` here. quality + latencyP95Ms are curated for all.
 */

export const BENCHMARK_SEED = {
  last_updated: '2026-05-01',
  methodologySources: [
    'Vendor-published pricing pages (2026-Q2)',
    'Public third-party evaluation leaderboards (quality)',
    'Curated latency sampling, p95 over 1k requests (third-party)',
  ],
  points: [
    // --- He models (price omitted — rendered from catalogue SoT) ---
    { modelId: 'qwen-max', vendorSlug: 'alibaba', vendor: 'Alibaba', isHe: true, quality: 85.2, latencyP95Ms: 920, source: 'Public eval leaderboard + curated latency sample' },
    { modelId: 'deepseek-v3', vendorSlug: 'deepseek', vendor: 'DeepSeek', isHe: true, quality: 88.1, latencyP95Ms: 760, source: 'Public eval leaderboard + curated latency sample' },
    { modelId: 'moonshot-v1-128k', vendorSlug: 'moonshot', vendor: 'Moonshot', isHe: true, quality: 82.4, latencyP95Ms: 1080, source: 'Public eval leaderboard + curated latency sample' },
    { modelId: 'glm-4', vendorSlug: 'zhipu', vendor: 'Zhipu', isHe: true, quality: 83.7, latencyP95Ms: 990, source: 'Public eval leaderboard + curated latency sample' },
    { modelId: 'doubao-pro', vendorSlug: 'bytedance', vendor: 'ByteDance', isHe: true, quality: 84.0, latencyP95Ms: 870, source: 'Public eval leaderboard + curated latency sample' },
    { modelId: 'ernie-4.0', vendorSlug: 'baidu', vendor: 'Baidu', isHe: true, quality: 83.1, latencyP95Ms: 1010, source: 'Public eval leaderboard + curated latency sample' },
    // --- non-He reference models (curated price — absent from catalogue) ---
    { modelId: 'gpt-4', vendorSlug: 'openai', vendor: 'OpenAI', isHe: false, quality: 89.6, latencyP95Ms: 1340, costPer1M: 30.0, source: 'OpenAI pricing page + public eval leaderboard' },
    { modelId: 'claude-3-5-sonnet', vendorSlug: 'anthropic', vendor: 'Anthropic', isHe: false, quality: 90.1, latencyP95Ms: 1120, costPer1M: 9.0, source: 'Anthropic pricing page + public eval leaderboard' },
    { modelId: 'gemini-1-5-pro', vendorSlug: 'google', vendor: 'Google', isHe: false, quality: 87.8, latencyP95Ms: 1260, costPer1M: 7.0, source: 'Google pricing page + public eval leaderboard' },
  ],
} as const;
