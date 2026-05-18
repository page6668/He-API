package seed

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// IssueKeyForTest is the t.Helper() wrapper around SeedAPIKey. Provided
// for parity with Story 3.2 callers (none survived the OQ1 refactor at
// the time of writing, but future suite tests can adopt the thin wrapper
// for the standard t.Fatal-on-error idiom).
func IssueKeyForTest(t *testing.T, ctx context.Context, q repository.Querier, userID uuid.UUID, name string) (plaintextKey string, apiKeyID uuid.UUID) {
	t.Helper()
	plaintextKey, apiKeyID, err := SeedAPIKey(ctx, q, userID, name)
	if err != nil {
		t.Fatalf("SeedAPIKey: %v", err)
	}
	return plaintextKey, apiKeyID
}
