// Package paymentmethod is billing-svc's single-writer for he_api.payment_methods
// (Story 7.7, Q-PAYMENT-METHODS — billing-svc owns the rows; payment-svc owns the
// Stripe SetupIntent that mints the token). It stores ONLY the opaque provider
// PaymentMethod token (provider_pm_token) — NEVER a card PAN (PCI §8.4 SAQ-A) —
// plus the display-safe brand/last4. The token is SECRET-grade: it is returned
// ONLY by GetOwnedToken (internal, for the off-session charge), never by List.
//
// Security invariants:
//   - Cross-user binding (BR-R-7 / threat_model #2): every read/charge path keys
//     on (id, user_id) so a foreign method id never resolves for another user.
//   - Revoke cascade (threat_model #1): deleting a method that an auto-recharge
//     references disables that auto-recharge (a dead token must not stay
//     charge-able). The FK is ON DELETE SET NULL; this store also flips
//     auto_recharge_enabled=false so no charge is attempted against a gone method.
package paymentmethod

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the minimal pgx surface this store needs (satisfied by *pgxpool.Pool and
// pgxmock.PgxPoolIface).
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrInvalid marks a rejected save (missing field).
var ErrInvalid = errors.New("paymentmethod: invalid input")

// Method is the DISPLAY-SAFE projection — it deliberately omits provider_pm_token.
type Method struct {
	ID        string `json:"id"`
	Provider  string `json:"payment_provider"`
	Brand     string `json:"brand"`
	Last4     string `json:"last4"`
	IsDefault bool   `json:"is_default"`
	CreatedAt string `json:"created_at"`
}

const (
	unsetDefaultsSQL = `UPDATE he_api.payment_methods SET is_default = FALSE WHERE user_id = $1`

	insertSQL = `INSERT INTO he_api.payment_methods
		(user_id, payment_provider, provider_pm_token, brand, last4, is_default, created_at)
	VALUES ($1, $2, $3, $4, $5, TRUE, NOW())
	RETURNING id::text`

	listSQL = `SELECT id::text, payment_provider, COALESCE(brand, ''), COALESCE(last4, ''), is_default,
		to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	FROM he_api.payment_methods WHERE user_id = $1 ORDER BY is_default DESC, created_at DESC`

	// owner-scoped token read (the ONLY path that returns the secret-grade token).
	getOwnedTokenSQL = `SELECT provider_pm_token, payment_provider
	FROM he_api.payment_methods WHERE id = $1 AND user_id = $2`

	// owner-scoped existence + provider (cross-user-binding guard for SetAutoRecharge).
	ownedProviderSQL = `SELECT payment_provider FROM he_api.payment_methods WHERE id = $1 AND user_id = $2`

	disableRefSQL = `UPDATE he_api.balances SET auto_recharge_enabled = FALSE
	WHERE user_id = $1 AND auto_recharge_payment_method_id = $2`

	deleteSQL = `DELETE FROM he_api.payment_methods WHERE id = $1 AND user_id = $2`
)

// Store persists payment methods.
type Store struct{ db DB }

// New builds a Store.
func New(db DB) *Store { return &Store{db: db} }

// Save persists a stored off-session token bound to userID and makes it the
// user's default (unsetting any prior default — at most one default per user,
// UNIT-013) in ONE transaction. Returns the new id. provider_pm_token is the
// opaque provider id; a PAN must never reach this method (PCI §8.4).
func (s *Store) Save(ctx context.Context, userID, provider, token, brand, last4 string) (id string, isDefault bool, err error) {
	userID = strings.TrimSpace(userID)
	provider = strings.TrimSpace(provider)
	token = strings.TrimSpace(token)
	if userID == "" || provider == "" || token == "" {
		return "", false, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("paymentmethod: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, unsetDefaultsSQL, userID); err != nil {
		return "", false, fmt.Errorf("paymentmethod: unset defaults: %w", err)
	}
	if err := tx.QueryRow(ctx, insertSQL, userID, provider, token, brand, last4).Scan(&id); err != nil {
		return "", false, fmt.Errorf("paymentmethod: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("paymentmethod: commit: %w", err)
	}
	return id, true, nil
}

// List returns the user's DISPLAY-SAFE methods (no token ever).
func (s *Store) List(ctx context.Context, userID string) ([]Method, error) {
	rows, err := s.db.Query(ctx, listSQL, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("paymentmethod: list: %w", err)
	}
	defer rows.Close()
	out := make([]Method, 0)
	for rows.Next() {
		var m Method
		if err := rows.Scan(&m.ID, &m.Provider, &m.Brand, &m.Last4, &m.IsDefault, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("paymentmethod: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("paymentmethod: rows: %w", err)
	}
	return out, nil
}

// GetOwnedToken returns the secret-grade token + provider for an owned method, or
// found=false if the id does not belong to userID (cross-user-binding guard).
func (s *Store) GetOwnedToken(ctx context.Context, userID, methodID string) (token, provider string, found bool, err error) {
	err = s.db.QueryRow(ctx, getOwnedTokenSQL, strings.TrimSpace(methodID), strings.TrimSpace(userID)).Scan(&token, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("paymentmethod: get token: %w", err)
	}
	return token, provider, true, nil
}

// OwnedProvider returns the provider of an owned method, or found=false if the id
// is not the user's (the SetAutoRecharge cross-user-binding guard, BR-R-7).
func (s *Store) OwnedProvider(ctx context.Context, userID, methodID string) (provider string, found bool, err error) {
	err = s.db.QueryRow(ctx, ownedProviderSQL, strings.TrimSpace(methodID), strings.TrimSpace(userID)).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("paymentmethod: owned provider: %w", err)
	}
	return provider, true, nil
}

// Delete revokes an owned method (IDOR-guarded). If the method was referenced by
// the user's auto-recharge, that auto-recharge is disabled first (revoke cascade).
// Returns deleted=false when the id is not the user's (gateway → 404).
func (s *Store) Delete(ctx context.Context, userID, methodID string) (deleted, autoRechargeDisabled bool, err error) {
	userID = strings.TrimSpace(userID)
	methodID = strings.TrimSpace(methodID)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, false, fmt.Errorf("paymentmethod: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	dis, err := tx.Exec(ctx, disableRefSQL, userID, methodID)
	if err != nil {
		return false, false, fmt.Errorf("paymentmethod: disable ref: %w", err)
	}
	autoRechargeDisabled = dis.RowsAffected() > 0

	del, err := tx.Exec(ctx, deleteSQL, methodID, userID)
	if err != nil {
		return false, false, fmt.Errorf("paymentmethod: delete: %w", err)
	}
	deleted = del.RowsAffected() > 0
	if !deleted {
		// Not the owner / not found — nothing removed; no cascade should stand.
		return false, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, fmt.Errorf("paymentmethod: commit: %w", err)
	}
	return deleted, autoRechargeDisabled, nil
}

// isUniqueViolation reports whether err is a Postgres unique_violation (23505) —
// used by the auto-recharge durable fence.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsUniqueViolation is the exported guard for callers in this module.
func IsUniqueViolation(err error) bool { return isUniqueViolation(err) }
