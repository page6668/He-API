// Package repository wraps PostgreSQL access for the users table (Story 2.2
// migration 0002_create_users.sql).
//
// Surfaces:
//   - InsertUser (idempotent on UNIQUE collision; returns ErrEmailExists so
//     the handler can apply BR-1.4 anti-enumeration response shape)
//   - GetUserByEmail (returns ErrUserNotFound when row absent — caller decides
//     dummy-bcrypt path per BR-3.2)
//   - UpdateEmailVerifiedAt (BR-2.3 idempotency)
//   - SoftLockUser (BR-3.3) + UnlockUser (BR-4.4 self-heal, transactional)
//
// All statements are parameterized; no string concatenation against user input
// (SEC-014 guard). The package never logs the password column value.
//
// P2-P4 materialize the implementation. P1 holds only this doc file.
package repository
