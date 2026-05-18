# API Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-18
**Total Stories Tracked**: 1
**Total Endpoints**: 2
**Repository**: He-API
**Mode**: monolith

## API Endpoints Registry

### Public HTTP routes (api-gateway)

| Method | Route | Auth | Story | Notes |
|--------|-------|------|-------|-------|
| POST | `/v1/chat/completions` | Bearer API key (`middleware.RequireAPIKey`) | 3.2 | **Placeholder 501** — returns `501_not_implemented` envelope until Story 3.3 ships the real chat handler. Story 3.2 wraps the placeholder with `bearerAuth.RequireAPIKey` to exercise the middleware integration path end-to-end. |

### Internal Connect/gRPC RPCs (auth-svc)

| Service | RPC | Story | Notes |
|---------|-----|-------|-------|
| `he.auth.v1.AuthService` | `ValidateApiKey(ValidateApiKeyRequest) returns (ValidateApiKeyResponse)` | 3.2 | Bcrypt-compare bearer-token plaintext against `he_api.api_keys` rows matching the 12-char `key_prefix`. Returns `ok=true` + identity / scope on match; `ok=false` with `reason ∈ {NOT_FOUND, REVOKED}` (anti-enumeration parity — gateway maps both to a single 401 envelope, BR-2.4). |

## Endpoints by Story

- **3.2** — Bearer-token API-key auth + key validation:
  - Public: `POST /v1/chat/completions` (placeholder, bearer-auth wrapped).
  - Internal: `he.auth.v1.AuthService/ValidateApiKey`.
