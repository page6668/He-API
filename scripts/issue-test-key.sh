#!/usr/bin/env bash
# Story 3.3 T3.5.4 — Issue a test API key by running the test-utility
# binary `apps/auth-svc/cmd/issue-test-key` and piping its stdout through.
#
# Required environment:
#   HE_API_DATABASE_URL  — postgres DSN
#   HE_API_TEST_USER_ID  — UUID of the owning user (must exist in he_api.users)
#
# Optional:
#   HE_API_TEST_KEY_NAME — display name (default: "test-key")
#
# Output: a single line on stdout — the plaintext bearer token (he-...).
# Errors surface on stderr; exit non-zero on failure.
set -euo pipefail

cd "$(dirname "$0")/.."
exec go run ./apps/auth-svc/cmd/issue-test-key
