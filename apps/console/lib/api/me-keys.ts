/**
 * Story 5.1 T7.5 — Zod schemas mirroring the gateway response shapes for
 * /v1/me/keys.
 *
 * Architect L-1 cascade — the Story-5.5 page component imports these
 * schemas to validate fetch() response bodies; on shape mismatch
 * `.safeParse` returns success=false and the page renders the empty-state
 * fallback (NEVER throws — degrade gracefully so a Story-1.x infra
 * hiccup doesn't break the Console).
 */
import { z } from "zod";

/**
 * KeyEntry mirrors apps/api-gateway/internal/handlers/me_keys_types.go
 * KeyEntry. Money fields are STRINGS (BR-2.10 / Architect Q-Spec-4 —
 * preserves NUMERIC(10,2) precision on round-trip). Nullable
 * timestamps render as JSON null (not omitted).
 */
// Canonical cap wire shape: NUMERIC(10,2) serialised as a 2-decimal string
// (Story 5.1 BR-2.10 / Q-Spec-4). Tightened per Story 5.5 BR-L-4 — the cap is a
// user-set value and the Zod parse is a safety net rejecting a non-canonical
// row rather than mis-rendering it. The server always emits canonical "NN.NN".
// NOTE: only the cap is tightened. current_month_cost_usd stays a loose string
// because the shipped Story-5.1 contract (5.1-me-keys-actions test) emits "0"
// for a zero balance — tightening it would reject a valid row.
const MONEY_RE = /^\d+\.\d{2}$/;

export const KeyEntrySchema = z.object({
  api_key_id: z.string().uuid(),
  name: z.string(),
  key_prefix: z.string(),
  scope: z.unknown(), // JSONB pass-through; read via readScope()
  monthly_cost_cap_usd: z.string().regex(MONEY_RE).nullable(),
  current_month_cost_usd: z.string(),
  last_used_at: z.string().nullable(),
  revoked_at: z.string().nullable(),
  created_at: z.string(),
});
export type KeyEntry = z.infer<typeof KeyEntrySchema>;

/**
 * KeyScope is the JSONB `scope` object. KeyEntry types it as `unknown`
 * (defensive pass-through). The Story-5.5 UI reads it through `readScope`,
 * which coerces any shape to the two arrays the table + drawer render —
 * never throwing on drift (a malformed scope degrades to "no restriction").
 */
export const KeyScopeSchema = z.object({
  models: z.array(z.string()).default([]),
  ip_whitelist: z.array(z.string()).default([]),
});
export type KeyScope = z.infer<typeof KeyScopeSchema>;

export function readScope(scope: unknown): KeyScope {
  const parsed = KeyScopeSchema.safeParse(scope ?? {});
  return parsed.success ? parsed.data : { models: [], ip_whitelist: [] };
}

/** ListKeysResponse envelope returned by GET /v1/me/keys. */
export const ListKeysResponseSchema = z.object({
  object: z.literal("list"),
  data: z.array(KeyEntrySchema),
});
export type ListKeysResponse = z.infer<typeof ListKeysResponseSchema>;

/**
 * CreateKeyResponse — POST /v1/me/keys returns the plaintext ONCE
 * (BR-1.5). The page rendering this MUST display + offer copy-to-
 * clipboard exactly once and then forget the plaintext.
 *
 * Field order parallels the Go struct (BR-1.13). Zod object schemas
 * do NOT enforce JSON byte-order at validation; the byte-order lock
 * is asserted at the Go boundary via 5.1-UNIT-031 / 5.1-INT-013.
 */
/**
 * Plaintext shape guard for the one-time-display sub-page (Story 5.5 BR-PD-4).
 *
 * The real key is `"he-" + base62(32 random bytes)`. base62 of 32 bytes is
 * normally 43 chars but leading-zero stripping makes it VARIABLE (worst case
 * ~42, very rarely 41) — see apps/auth-svc/internal/apikey/generate.go. The
 * SM's fixed-43 regex would 404 a valid 42-char key, so the guard tolerates
 * 41–43 base62 chars: tight enough to reject junk deep-links, loose enough to
 * never reject a real key.
 */
export const PLAINTEXT_RE = /^he-[0-9A-Za-z]{41,43}$/;

export const CreateKeyResponseSchema = z.object({
  api_key_id: z.string().uuid(),
  key_prefix: z.string(),
  name: z.string(),
  plaintext: z.string(),
  created_at: z.string(),
  warning: z.string(),
});
export type CreateKeyResponse = z.infer<typeof CreateKeyResponseSchema>;

/** RevokeKeyResponse — DELETE /v1/me/keys/{id} (BR-3.11 field order). */
export const RevokeKeyResponseSchema = z.object({
  api_key_id: z.string().uuid(),
  revoked_at: z.string(),
  was_already_revoked: z.boolean(),
});
export type RevokeKeyResponse = z.infer<typeof RevokeKeyResponseSchema>;

/**
 * UpdateKeyResponse — PATCH /v1/me/keys/{id} (Story 5.2 BR-1.13 field order).
 * Mirrors apps/api-gateway/internal/handlers/me_keys_update_types.go
 * UpdateKeyResponse (byte-identical to KeyEntry so the Story-5.5 listing
 * re-renders the mutated row with the same component). Degrades gracefully
 * on shape drift (.safeParse → success=false; never throws).
 */
export const UpdateKeyResponseSchema = z.object({
  api_key_id: z.string().uuid(),
  name: z.string(),
  key_prefix: z.string(),
  scope: z.unknown(),
  monthly_cost_cap_usd: z.string().nullable(),
  current_month_cost_usd: z.string(),
  last_used_at: z.string().nullable(),
  revoked_at: z.string().nullable(),
  created_at: z.string(),
});
export type UpdateKeyResponse = z.infer<typeof UpdateKeyResponseSchema>;

/**
 * KeyConfigPatch is the PATCH request body (Story 5.2). All fields optional —
 * only present fields mutate (BR-1.7). monthly_cost_cap_usd is a string-decimal
 * or null (clear the cap); a JSON number is rejected server-side (BR-1.6).
 */
export const KeyConfigPatchSchema = z.object({
  scope: z
    .object({
      models: z.array(z.string()).optional(),
      ip_whitelist: z.array(z.string()).optional(),
    })
    .optional(),
  monthly_cost_cap_usd: z.string().nullable().optional(),
});
export type KeyConfigPatch = z.infer<typeof KeyConfigPatchSchema>;

/**
 * KeyName validator — parity with Story-2.5 BR-2.3 display_name (per
 * Architect Q10 ratified L-1 share-by-import cascade). 1-100 runes
 * after NFC; allowed code-point classes L/M/N/P/Sc/space.
 *
 * NOTE: full unicode-class enforcement is server-side (auth-svc
 * ValidateKeyName). The client-side Zod runs a lighter check —
 * length + non-empty + trim — to give a fast UX feedback loop;
 * the server is the source of truth.
 */
export const KeyNameSchema = z
  .string()
  .trim()
  .min(1, { message: "account.keys.errors.name.missing" })
  .max(100, { message: "account.keys.errors.name.too_long" })
  // Story 5.5 BR-C-1 — mirror Story 5.1 BR-1.7 allowed code-point classes
  // (Letter / Mark / Number / Punctuation / Currency-symbol / ASCII space).
  // Control / format / surrogate / private-use / other-symbol (incl. emoji)
  // are rejected. Defence-in-depth — auth-svc ValidateKeyName is authoritative.
  .regex(/^[\p{L}\p{M}\p{N}\p{P}\p{Sc} ]+$/u, {
    message: "account.keys.errors.name.invalid_chars",
  })
  .transform((s) => s.normalize("NFC"));
