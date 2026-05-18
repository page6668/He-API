// Package apikey implements the Story 3.2 AC2 ValidateApiKey RPC.
//
// Hot-path contract: bcrypt-compare the supplied plaintext bearer token
// against rows that share its 12-char key_prefix; first match wins. Anti-
// enumeration parity (BR-2.4) between REVOKED and NOT_FOUND is enforced at
// the auth-svc/api-gateway boundary — the `reason` enum is internal-only.
//
// Defence in depth:
//   - Syntactic regex `^he-[A-Za-z0-9]{10,253}$` is applied BEFORE any DB
//     query (BR-2.6) so an attacker cannot incur bcrypt cost via oversized
//     inputs.
//   - The plaintext key MUST NOT appear in any slog field, OTel span
//     attribute, or returned error message (TC-9 + BR-2.10 — manual code
//     review enforced; golangci-lint custom rule pending Epic 9 hardening).
//
// `seed_test.go` (not linked into the production binary by Go's `_test.go`
// rule) exposes IssueKeyForTest for the integration test suite — Story 3.2's
// only "issue key" UX until Epic 5 lands the real flow.
package apikey
