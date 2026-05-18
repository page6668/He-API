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
// The Story-3.2 IssueKeyForTest helper was relocated to the
// `apps/auth-svc/internal/apikey/seed` sub-package (Story 3.3 OQ1 ruling)
// so a CI test-utility binary (`apps/auth-svc/cmd/issue-test-key`) can
// import the helper without dragging it into a `_test.go` file. The
// Story 3.2 BR-2.8 invariant ("no production binary may issue keys") is
// re-enforced via the `apikey-seed-boundary-guard` import-graph CI job
// (Story 3.3 T3.6) — see `.github/workflows/lint.yml`.
package apikey
