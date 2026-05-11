package middleware

// JWTVerify (Story 2.5+ surface) — verifies the `he_access` cookie on
// protected routes; auth-svc public key is loaded from the ConfigMap
// `he-api-auth-public-keys` (Story 2.2 T0.5).
//
// P1 holds only this comment so the package compiles and the path exists.
// Activation lands in Story 2.5 when the first protected route appears.
