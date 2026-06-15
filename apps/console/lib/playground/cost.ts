/**
 * Story 10.6 — Playground cost ESTIMATE (BR-10.6.5, OQ-10.6-3).
 *
 * cost = catalogue pricing × returned `usage` tokens. The result is a DISPLAY
 * estimate, ALWAYS labeled 估算/estimated, and MUST NEVER be presented as the
 * billed amount. This module deliberately does NOT read the X-He-Cost-Usd header
 * (ABSENT — OQ5) nor the usage_ledger (the post-hoc async money SoT, H-1-R):
 * `usage_ledger` stays the single money source of truth.
 * [[project_cost_source_oq5_usage_ledger]]
 */
import { pricingFor } from '@/lib/catalogue/pricing';

export interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens?: number;
}

export interface CostEstimate {
  /** Estimated USD amount (input×prompt + output×completion) / 1e6. */
  amountUsd: number;
  /** ALWAYS true — this is an estimate, never a billed figure. */
  estimated: true;
  /** Where the unit price came from (the console catalogue SoT, never a ledger). */
  source: 'catalogue';
}

/**
 * Estimate the USD cost of a single completion from catalogue pricing × usage.
 * Returns null when the model has no catalogue pricing (the UI then shows no cost,
 * never a fabricated or billed figure).
 */
export function estimatePlaygroundCost(modelId: string, usage: Usage): CostEstimate | null {
  const pricing = pricingFor(modelId);
  if (!pricing) {
    return null;
  }
  const prompt = Math.max(0, usage.prompt_tokens || 0);
  const completion = Math.max(0, usage.completion_tokens || 0);
  const amountUsd =
    (prompt * pricing.inputPerMTokens + completion * pricing.outputPerMTokens) / 1_000_000;
  return { amountUsd, estimated: true, source: 'catalogue' };
}

/** Format an estimate for display, e.g. "$0.000123 (estimated)". The label text
 *  is supplied by the caller (i18n `output.estimated`) so it localizes. */
export function formatEstimate(estimate: CostEstimate | null, estimatedLabel: string): string {
  if (!estimate) {
    return '—';
  }
  return `$${estimate.amountUsd.toFixed(6)} (${estimatedLabel})`;
}
