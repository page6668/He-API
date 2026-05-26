// Story 5.3 — TokenDeducter interface decoupling handlers from the
// ratelimit middleware package.
//
// Handlers depend on the narrow interface; production wiring in
// cmd/server/main.go passes the *ratelimit.Middleware concrete type
// (which carries TPMDeduct as a method, satisfying this interface
// implicitly). Tests substitute a captured-args fake.
//
// PII discipline (Story 5.3 BR-3.10 / BR-X.6): tokens is an integer
// count; NEVER message content. NEVER plaintext keys.

package handlers

import "context"

// TokenDeducter is the post-response hook called by chat-completions
// and embeddings handlers after upstream usage is known. Fire-and-
// forget — the response has already been written; errors are absorbed
// by the deducter (slog WARN + Prometheus counter) and never surface
// to the client.
type TokenDeducter interface {
	TPMDeduct(ctx context.Context, apiKeyID string, tokens int)
}

// nopTokenDeducter is the default when no deducter is wired (tests +
// pre-Story-5.3 startup). Drops every call silently.
type nopTokenDeducter struct{}

func (nopTokenDeducter) TPMDeduct(context.Context, string, int) {}
