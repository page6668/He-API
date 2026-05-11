// Package token isolates email-verification token generation (CSPRNG
// 32 bytes, base64url-encoded) and Redis-backed storage (TS-CONS-006).
//
// Invariants:
//   - crypto/rand only (math/rand banned)
//   - Plaintext token is returned ONCE for inclusion in the verification email;
//     storage key uses lowercase-hex SHA-256(token) — plaintext never written
//     to Redis, PG, logs, or audit
//   - Redis key: auth:email_verify:{sha256(token)} → JSON{user_id, expires_at,
//     attempts}, TTL=86400s (24h)
//   - DEL-after-use enforces one-shot consumption; attempts ≥ 5 trips
//     ErrTokenAttemptsExceeded + immediate DEL
//
// P3 (T2, AC2) materializes the implementation. P1 holds only this doc file.
package token
