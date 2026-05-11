// Package jwt isolates JWT RS256 signing + verification, claim minimization,
// and refresh-token family rotation.
//
// Invariants (TS-CONS-002, BR-3.5, BR-3.6, BR-3.9):
//   - Algorithm: RS256 (RSA 4096-bit) — HS256 and "none" rejected on verify
//   - Access-token claims: {sub, iat, exp, jti, aud="he-api"} only — no email,
//     locale, role, or scope (those are looked up by api-gateway from PG/cache)
//   - Access TTL: 15 min; Refresh TTL: 30 days
//   - Refresh rotation: DEL old jti + SET new jti atomically in Redis
//     family bucket; reuse triggers family revocation
//
// P4 (T3, AC3) materializes the implementation. P1 holds only this doc file.
package jwt
