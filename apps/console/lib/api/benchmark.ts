/**
 * Story 10.6 — Benchmark data schema + loader (BR-10.6.9/.11, AC2).
 *
 * Zod `.safeParse` validates the curated seed (4.7 / 9.1b precedent): on a shape
 * drift it returns a graceful-degrade result (no crash / no 500) so the page can
 * render a fallback banner. He-model prices are rendered FROM the catalogue SoT
 * (`lib/catalogue/pricing`), NEVER from the seed (BR-10.6.11 / BLIND-DATA-003);
 * only the non-He reference models carry a curated price.
 */
import { z } from 'zod';

import { pricingFor } from '@/lib/catalogue/pricing';
import { BENCHMARK_SEED } from '@/lib/benchmark/seed';

export const BenchmarkPointSchema = z.object({
  modelId: z.string().min(1),
  vendorSlug: z.string().min(1),
  vendor: z.string().min(1),
  isHe: z.boolean(),
  quality: z.number(),
  latencyP95Ms: z.number().nonnegative(),
  costPer1M: z.number().nonnegative().optional(),
  source: z.string().min(1),
});
export type BenchmarkPoint = z.infer<typeof BenchmarkPointSchema>;

export const BenchmarkSeedSchema = z.object({
  last_updated: z.string().min(1),
  methodologySources: z.array(z.string().min(1)).min(1),
  points: z.array(BenchmarkPointSchema).min(1),
});
export type BenchmarkSeed = z.infer<typeof BenchmarkSeedSchema>;

/** A display row: a point with its cost-per-1M RESOLVED (He ← catalogue). */
export interface BenchmarkRow {
  modelId: string;
  vendorSlug: string;
  vendor: string;
  isHe: boolean;
  quality: number;
  latencyP95Ms: number;
  costPer1M: number;
  source: string;
}

export interface BenchmarkData {
  lastUpdated: string;
  methodologySources: string[];
  rows: BenchmarkRow[];
}

/** Blended He price (per 1M tokens) = mean of input+output, from the catalogue SoT. */
function heCostPer1M(modelId: string): number | null {
  const p = pricingFor(modelId);
  if (!p) return null;
  return Math.round(((p.inputPerMTokens + p.outputPerMTokens) / 2) * 100) / 100;
}

/**
 * Resolve every point's cost: He models ALWAYS from the catalogue SoT (the seed
 * never overrides — BLIND-DATA-003); non-He from the curated seed. A He model
 * missing from the catalogue is dropped (graceful degrade, BLIND-BOUNDARY-005).
 */
export function assembleBenchmark(seed: BenchmarkSeed): BenchmarkData {
  const rows: BenchmarkRow[] = [];
  for (const p of seed.points) {
    let cost: number | null;
    if (p.isHe) {
      cost = heCostPer1M(p.modelId); // catalogue SoT — seed price (if any) ignored
    } else {
      cost = typeof p.costPer1M === 'number' ? p.costPer1M : null;
    }
    if (cost === null) {
      continue; // missing price → drop the row rather than render a fabricated 0
    }
    rows.push({
      modelId: p.modelId,
      vendorSlug: p.vendorSlug,
      vendor: p.vendor,
      isHe: p.isHe,
      quality: p.quality,
      latencyP95Ms: p.latencyP95Ms,
      costPer1M: cost,
      source: p.source,
    });
  }
  return { lastUpdated: seed.last_updated, methodologySources: [...seed.methodologySources], rows };
}

export type LoadBenchmarkResult =
  | { ok: true; data: BenchmarkData }
  | { ok: false };

/**
 * Validate + assemble the benchmark data. Never throws — a shape drift degrades
 * to { ok: false } so the page renders a fallback banner (not a blank page / 500).
 */
export function loadBenchmarkData(rawSeed: unknown = BENCHMARK_SEED): LoadBenchmarkResult {
  const parsed = BenchmarkSeedSchema.safeParse(rawSeed);
  if (!parsed.success) {
    console.warn(
      `[benchmark] event=benchmark_shape_drift issues=${JSON.stringify(parsed.error.issues)}`,
    );
    return { ok: false };
  }
  return { ok: true, data: assembleBenchmark(parsed.data) };
}
