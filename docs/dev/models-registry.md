# Models & Types Cumulative Registry

> Auto-generated on first story creation
> Updated by Dev Agent after each story completion

## Registry Metadata

**Last Updated**: 2026-05-18
**Total Stories Tracked**: 1
**Total Models**: 5
**Repository**: He-API
**Mode**: monolith

## Protobuf Messages (`packages/proto/he/auth/v1/auth.proto`)

| Message | Story | Notes |
|---------|-------|-------|
| `ValidateApiKeyRequest` | 3.2 | `plaintext_key` (string) + `client_ip` (string) + `user_agent` (string). Client-attribution fields are informational only — auth-svc forwards them for future audit-svc logging. |
| `ValidateApiKeyResponse` | 3.2 | `ok` (bool) + `api_key_id` / `user_id` / `team_id` / `scope` (all string, populated only when ok=true) + `reason` (enum). |

## Protobuf Enums

| Enum | Story | Values |
|------|-------|--------|
| `ApiKeyValidationReason` | 3.2 | `API_KEY_VALIDATION_REASON_UNSPECIFIED` (ok=true) · `API_KEY_VALIDATION_REASON_NOT_FOUND` · `API_KEY_VALIDATION_REASON_REVOKED`. **Internal-only** — gateway maps both NOT_FOUND and REVOKED to a single 401 envelope per BR-2.4. |

## Go Repository Types (`apps/auth-svc/internal/repository`)

| Type | Story | Notes |
|------|-------|-------|
| `ApiKeyRow` | 3.2 | Mirrors `he_api.api_keys` for the Validate hot path. Nullable columns surface as `pgtype.UUID` / `pgtype.Timestamptz` so the handler distinguishes "no team" from "empty UUID". |

## Models by Story

- **3.2** — Bearer-token API-key auth:
  - `ValidateApiKeyRequest` / `ValidateApiKeyResponse` / `ApiKeyValidationReason` (proto).
  - `ApiKeyRow` (Go repository type).
  - `middleware.CachedClaims` (gateway-local JSON cache envelope; not part of the cross-service contract).
