// Story 3.3 T3.5.3 — issue-test-key is a test-utility binary that seeds
// a single he_api.api_keys row for the supplied user and prints the
// plaintext bearer token on stdout (one line, terminated by `\n`).
//
// Designed for the CI `gateway / openai-sdk-contract` job + dev-bootstrap
// `scripts/issue-test-key.sh`. NOT a production binary — guarded by the
// `apikey-seed-boundary-guard` lint workflow (Story 3.3 T3.6) which
// fails the build if `apps/api-gateway/cmd/server` or
// `apps/auth-svc/cmd/server` link `apps/auth-svc/internal/apikey`.
//
// Required environment variables:
//
//	HE_API_DATABASE_URL  — postgres DSN (e.g. postgres://he_api:...@host:5432/he_api?sslmode=disable)
//	HE_API_TEST_USER_ID  — UUID of the owning user (must exist in he_api.users)
//
// Optional:
//
//	HE_API_TEST_KEY_NAME — display name for the key row (default: "test-key")
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/he-api/he-api/apps/auth-svc/internal/apikey/seed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "issue-test-key:", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("HE_API_DATABASE_URL")
	if dsn == "" {
		return errors.New("HE_API_DATABASE_URL is required")
	}
	rawUserID := os.Getenv("HE_API_TEST_USER_ID")
	if rawUserID == "" {
		return errors.New("HE_API_TEST_USER_ID is required (UUID of the owning user)")
	}
	userID, err := uuid.Parse(rawUserID)
	if err != nil {
		return fmt.Errorf("parse HE_API_TEST_USER_ID: %w", err)
	}
	name := os.Getenv("HE_API_TEST_KEY_NAME")
	if name == "" {
		name = "test-key"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}

	plaintext, _, err := seed.SeedAPIKey(ctx, pool, userID, name)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	// stdout contract: one plaintext key, single `\n` terminator. The
	// shell wrapper at scripts/issue-test-key.sh pipes this verbatim.
	fmt.Println(plaintext)
	return nil
}
