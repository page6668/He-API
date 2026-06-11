/**
 * Story 9.1 AC2/AC3 — Zod schema mirroring the gateway GET /v1/me/usage/summary
 * response. Money + success_rate are STRINGS (Q-Spec-4 string-decimal — a JSON
 * number would lose precision); success_rate is nullable (requests=0 → null →
 * the UI renders "—", never NaN). cost_usd is the billed usage_ledger SUM
 * (H-1-R) and is nullable: a usage_ledger read error renders it null → "—" while
 * the ClickHouse-sourced cells still render (R2-4 independent degradation).
 *
 * `.safeParse` is a safety net: on shape drift the page degrades gracefully
 * (ErrorBoundary / dashboard.errors.malformed) rather than throwing.
 * [The UsageSeries schema is DEFERRED to 9.1b — Architect H-4.]
 */
import { z } from 'zod';

export const TokenBreakdownSchema = z.object({
  prompt: z.number().int(),
  completion: z.number().int(),
  total: z.number().int(),
});
export type TokenBreakdown = z.infer<typeof TokenBreakdownSchema>;

export const UsagePeriodSchema = z.object({
  requests: z.number().int(),
  success_rate: z.string().nullable(), // "[0,1]" string-decimal OR null when requests=0
  tokens: TokenBreakdownSchema,
  cost_usd: z.string().nullable(), // usage_ledger SUM string-decimal (H-1-R) OR null on a usage_ledger read error (R2-4)
});
export type UsagePeriod = z.infer<typeof UsagePeriodSchema>;

export const UsageSummarySchema = z.object({
  today: UsagePeriodSchema,
  month: UsagePeriodSchema,
  quarter: UsagePeriodSchema,
});
export type UsageSummary = z.infer<typeof UsageSummarySchema>;

/** The three period keys, in display order. */
export const PERIOD_ORDER = ['today', 'month', 'quarter'] as const;
export type PeriodKey = (typeof PERIOD_ORDER)[number];
