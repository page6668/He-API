/**
 * Story 4.7 — `GET /public/models` client + Zod schema (T4.1).
 *
 * Architect Round 1 rulings honoured:
 *
 *   - OQ-4.7-8 — `next: { revalidate: 300 }` 5-minute ISR by default.
 *     m-3 optional refinement: 60s in non-production for faster feedback
 *     during local dev (acceptable per Architect advisory).
 *
 * Failure modes (AC2 error-handling):
 *
 *   - Upstream non-2xx → return an empty matrix; the page renders the
 *     fallback banner (search-engine de-indexing risk guard).
 *   - Upstream 2xx with malformed JSON / Zod schema mismatch → same path;
 *     server-side console.warn fires with `event=public_models_shape_drift`
 *     so dev-server logs (or production observability hooks) surface the
 *     drift.
 */
import { z } from "zod";

/** BR-1.2 capability schema — mirrors the Go-side ModelCapabilities. */
export const ModelCapabilitiesSchema = z.object({
  chat: z.boolean(),
  streaming: z.boolean(),
  function_calling: z.boolean(),
  vision: z.boolean(),
  json_mode: z.boolean(),
  context_window_tokens: z.number().int().nonnegative(),
  max_output_tokens: z.number().int().nonnegative(),
});
export type ModelCapabilities = z.infer<typeof ModelCapabilitiesSchema>;

/** BR-1.4 ModelEntry shape — capability as the last field. */
export const ModelEntrySchema = z.object({
  id: z.string(),
  object: z.literal("model"),
  created: z.number().int(),
  owned_by: z.string(),
  capabilities: ModelCapabilitiesSchema,
});
export type ModelEntry = z.infer<typeof ModelEntrySchema>;

/** ModelList envelope returned by /public/models. */
export const PublicModelsResponseSchema = z.object({
  object: z.literal("list"),
  data: z.array(ModelEntrySchema),
});
export type PublicModelsResponse = z.infer<typeof PublicModelsResponseSchema>;

/** Default base URL when HE_API_PUBLIC_BASE_URL is unset (Story 3.x dev port). */
const DEFAULT_BASE_URL = "http://localhost:8080";

function resolveBaseURL(): string {
  return process.env.HE_API_PUBLIC_BASE_URL ?? DEFAULT_BASE_URL;
}

function resolveRevalidate(): number {
  // m-3 advisory — faster cache turnover during local development so
  // capability-table edits surface within a minute.
  if (process.env.NODE_ENV !== "production") {
    return 60;
  }
  return 300;
}

/**
 * Fetch the public capability matrix. Never throws — failures collapse to
 * an empty list so the page can render its fallback banner without an
 * uncaught Server-Component exception.
 */
export async function fetchPublicModels(): Promise<PublicModelsResponse> {
  const url = `${resolveBaseURL().replace(/\/$/, "")}/public/models`;
  try {
    const res = await fetch(url, { next: { revalidate: resolveRevalidate() } });
    if (!res.ok) {
      console.warn(
        `[public_models] upstream non-2xx: status=${res.status} url=${url}`,
      );
      return { object: "list", data: [] };
    }
    const json = await res.json();
    const parsed = PublicModelsResponseSchema.safeParse(json);
    if (!parsed.success) {
      // Server-side observability hook — Sentry / OTEL slog can pick this
      // up via console.warn in production. event=public_models_shape_drift
      // anchors the AC2 error-handling row 2 telemetry expectation.
      console.warn(
        `[public_models] event=public_models_shape_drift url=${url} issues=${JSON.stringify(parsed.error.issues)}`,
      );
      return { object: "list", data: [] };
    }
    return parsed.data;
  } catch (err) {
    console.warn(
      `[public_models] fetch failed: url=${url} error=${(err as Error).message}`,
    );
    return { object: "list", data: [] };
  }
}
