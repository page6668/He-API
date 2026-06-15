// Story 10.7 (L-2) — the single TypeScript source of truth for the §11.5
// `he_request_id` shape. §11.5 registers `he.request_id` as the customer-support
// correlation handle with the exact regex `^req_[a-f0-9]{12}$` (an opaque,
// NON-PII request fingerprint). The console-side support-handle passing
// validates against THIS constant rather than re-declaring the literal, so the
// shape can never drift from the registry (Architect Low-Issue L-2).
//
// [Source: docs/architecture/11-可观测性observability.md §11.5:96]

export const HE_REQUEST_ID_REGEX = /^req_[a-f0-9]{12}$/;

export function isHeRequestId(value: unknown): value is string {
  return typeof value === 'string' && HE_REQUEST_ID_REGEX.test(value);
}
