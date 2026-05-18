// Package openaierr emits the canonical OpenAI-compatible §5.1.2 error
// envelope for the He-API gateway.
//
// The single exported writer Write delegates per-code metadata to
// codeMetadata (codes.go) and reads the per-request he_request_id from
// middleware/requestid via requestid.FromContext.
//
// See docs/architecture/rest-api-spec.md §5.1.2 for the canonical code
// taxonomy (16 active rows + 1 RETIRED row preserved for git-blame; the
// RETIRED row is OMITTED from codeMetadata per BR-1.4 + T0.2).
//
// Story 3.6 — Standardized Error Responses + he_request_id.
package openaierr
