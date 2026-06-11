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

/**
 * Story 9.2 AC2 — Zod schema mirroring the gateway GET /v1/me/usage/logs page.
 * Each LogEntry carries ONLY the 13 non-PII observability fields the gateway
 * projects (BR-RD-9); client_ip / client_country / user_agent / error_message /
 * cost_usd are intentionally absent (cost is non-authoritative — H-1-R / BR-UI-4).
 * `.safeParse` is the safety net: shape drift → the page degrades to a generic
 * error state rather than throwing (BR-UI-2).
 */
export const LogEntrySchema = z.object({
  he_request_id: z.string(),
  ts: z.string(), // RFC3339 (the gateway encodes time.Time as a JSON string)
  model: z.string(),
  upstream_model: z.string(),
  status_code: z.number().int(),
  is_streaming: z.boolean(),
  prompt_tokens: z.number().int(),
  completion_tokens: z.number().int(),
  total_tokens: z.number().int(),
  latency_ms_total: z.number().int(),
  ttfb_ms: z.number().int(),
  api_key_id: z.string(),
  error_code: z.string(),
});
export type LogEntry = z.infer<typeof LogEntrySchema>;

export const UsageLogsPageSchema = z.object({
  items: z.array(LogEntrySchema),
  total_count: z.number().int(),
  limit: z.number().int(),
  offset: z.number().int(),
  has_more: z.boolean(),
});
export type UsageLogsPage = z.infer<typeof UsageLogsPageSchema>;

/** Canonical status classes (mirror the gateway BR-RD-6 enum). */
export const LOG_STATUS_CLASSES = ['success', 'client_error', 'server_error'] as const;
export type LogStatusClass = (typeof LOG_STATUS_CLASSES)[number];

/** The page-size options offered by the limit selector (BR-UI Data Validation). */
export const LOG_LIMIT_OPTIONS = [25, 50, 100] as const;
export const LOG_DEFAULT_LIMIT = 50;

/** The 1000-row recent-window ceiling enforced by the gateway (BR-RD-2). */
export const LOG_WINDOW_CAP = 1000;
