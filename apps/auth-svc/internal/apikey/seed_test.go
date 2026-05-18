package apikey

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// IssueKeyForTest seeds a single he_api.api_keys row owned by the supplied
// user and returns the plaintext bearer token + the inserted row's id.
//
// Per AC2 BR-2.8 this helper lives in a `_test.go` file so it cannot link
// into the production binary; the Story 5 issuance UX supersedes it.
// bcrypt cost is fixed at 4 (NOT 12) — a 12-cost hash takes ~250 ms per
// call which would kill parallel-test throughput.
//
// Returned plaintext has the canonical `he-` + 43-char base62 body shape
// (KeyPrefixLength=12 → first 12 chars = "he-" + 9 random chars).
func IssueKeyForTest(t *testing.T, ctx context.Context, q repository.Querier, userID uuid.UUID, name string) (plaintextKey string, apiKeyID uuid.UUID) {
	t.Helper()
	if name == "" {
		name = "test-key"
	}

	// Build a base62-ish body from base64 raw bytes — strip the two
	// non-base62 chars `-`/`_` deterministically so the regex
	// `^he-[A-Za-z0-9]{10,253}$` always matches.
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw[:])
	body = strings.NewReplacer("-", "A", "_", "B").Replace(body) // ensure base62
	plaintextKey = "he-" + body

	prefix := plaintextKey[:KeyPrefixLength]

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintextKey), 4)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}

	apiKeyID, err = repository.InsertAPIKeyForTest(ctx, q, userID, name, prefix, string(hash))
	if err != nil {
		t.Fatalf("InsertAPIKeyForTest: %v", err)
	}
	return plaintextKey, apiKeyID
}
