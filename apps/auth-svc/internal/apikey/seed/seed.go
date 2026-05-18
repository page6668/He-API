// Package seed exposes the API-key seeding helper for CI / dev-bootstrap
// scripts and Story-3.2 test suites.
//
// Story 3.3 OQ1 ruling — SeedAPIKey lives in a dedicated sub-package
// (`apps/auth-svc/internal/apikey/seed`) so the import-graph CI guard
// (`apikey-seed-boundary-guard` in .github/workflows/lint.yml) can use a
// package-level grep to detect any production binary that links it. The
// sibling `apikey` package (containing the ValidateApiKey Service) is
// linked by `apps/auth-svc/cmd/server` and intentionally remains
// production-importable — keeping the seed helper in the same package
// would render the guard meaningless because Service IS production code.
//
// Story 3.2 BR-2.8 invariant is preserved: only the test-utility binary
// `apps/auth-svc/cmd/issue-test-key` and `_test.go` files may import
// this package. The lint guard fails the build if
// `apps/api-gateway/cmd/server` or `apps/auth-svc/cmd/server` acquire a
// dependency on `apps/auth-svc/internal/apikey/seed`.
//
// bcrypt cost is fixed at 4 (NOT 12) — a 12-cost hash takes ~250 ms per
// call which would kill parallel-test throughput. CI-issued keys
// accept this cost; production key issuance (Epic 5) will use the
// project-default cost.
package seed

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/he-api/he-api/apps/auth-svc/internal/apikey"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// SeedAPIKey inserts a single he_api.api_keys row owned by `userID` and
// returns the canonical `he-` + 43-char base62 plaintext bearer token
// along with the inserted row's id. Errors from rand.Read / bcrypt /
// repository.InsertAPIKeyForTest are wrapped and returned — no panic, no
// `t.Fatal`, so non-test callers (CI / dev-bootstrap utility binaries)
// can handle failures cleanly.
//
// The plaintext shape matches the production regex
// `^he-[A-Za-z0-9]{10,253}$` enforced by `apikey.Validate` so a key issued
// here passes the same syntactic gate as a real Epic-5-issued key.
func SeedAPIKey(ctx context.Context, q repository.Querier, userID uuid.UUID, name string) (plaintextKey string, apiKeyID uuid.UUID, err error) {
	if name == "" {
		name = "test-key"
	}

	// Base62-safe body: base64.RawURL on 32 bytes → 43 chars; substitute
	// the two non-base62 runes deterministically so the prefix-aware
	// regex never trips on a hyphen/underscore.
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", uuid.Nil, fmt.Errorf("seed: rand.Read: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw[:])
	body = strings.NewReplacer("-", "A", "_", "B").Replace(body)
	plaintextKey = "he-" + body

	prefix := plaintextKey[:apikey.KeyPrefixLength]

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintextKey), 4)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("seed: bcrypt.GenerateFromPassword: %w", err)
	}

	apiKeyID, err = repository.InsertAPIKeyForTest(ctx, q, userID, name, prefix, string(hash))
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("seed: InsertAPIKeyForTest: %w", err)
	}
	return plaintextKey, apiKeyID, nil
}
