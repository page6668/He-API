package middleware

// CSRF (Story 2.2 BR-4.6) — Origin / Referer allowlist enforced on
// state-mutating POSTs. P5 / T4 materializes the full implementation:
//
//   - Allowlist: he-api.com + *.he-api.com + localhost (dev only)
//   - Rejection: opaque 403 (no user-readable message — BR-4.6 audit-only)
//   - Audit: emit auth.csrf_violation event on rejection
//
// P1 holds only this comment so the package compiles and the path exists.
