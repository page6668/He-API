// Package requestid is the per-request he_request_id stamping middleware
// for the He-API gateway.
//
// The exported RequestID middleware derives a per-request identifier from
// the OTel SpanContext (first 6 bytes of TraceID, hex-encoded) and stamps
// it on (i) the response header X-He-Request-Id, (ii) the request context
// (accessible via FromContext), and (iii) the OTel span attribute
// he.request_id, per docs/architecture/rest-api-spec.md §5.1.1 and §11.5.
//
// The package sits as a subpackage under middleware/ (per Architect Round 1
// OQ6 ratification) so openaierr can import requestid without pulling in
// the full middleware package's dependencies (bearer_auth, jwt_verify,
// csrf, oauth_ratelimit).
//
// Story 3.6 — Standardized Error Responses + he_request_id.
package requestid
