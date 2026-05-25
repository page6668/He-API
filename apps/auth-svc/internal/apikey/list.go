// Story 5.1 T2.1 — ListApiKeys RPC handler.
//
// Returns up to 100 api_keys rows owned by req.UserId, ordered by
// created_at DESC then id ASC for deterministic tie-breaks (BR-2.3).
// REVOKED keys are INCLUDED (BR-2.4 — the UI needs to show historical
// revocations for audit; the API is honest, the UI is free to filter).
//
// Defence-in-depth (BR-2.5): the underlying SELECT explicitly OMITS the
// `key_hash` column at the SQL boundary. The proto ApiKeyEntry message
// also OMITS the field; UNIT-016 grep-asserts the marshaled bytes contain
// no `key_hash` substring.

package apikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// numericToDecimalString renders a pgtype.Numeric as its canonical decimal
// string (e.g., "50.00"). Caller MUST check .Valid first; this function
// returns "0" on the !Valid path (defensive — should never be reached
// because the caller already branched on Valid).
//
// Uses the same numeric→text path as pgtype.Numeric.MarshalJSON minus the
// NaN special-casing (we never insert NaN into api_keys money columns —
// they are GENERATED ALWAYS … or DEFAULT 0 / nullable).
func numericToDecimalString(n pgtype.Numeric) string {
	if !n.Valid {
		return "0"
	}
	if n.NaN {
		return "0" // unreachable for api_keys columns; defensive
	}
	// MarshalJSON returns the un-quoted numeric bytes (e.g., "50.00") which
	// is exactly what we want as a string.
	b, err := n.MarshalJSON()
	if err != nil {
		return "0"
	}
	return string(b)
}

// ListApiKeys is the Story-5.1 AC2 RPC handler.
func (s *Service) ListApiKeys(ctx context.Context, req *authv1.ListApiKeysRequest) (*authv1.ListApiKeysResponse, error) {
	ctx, span := s.startSpanNamed(ctx, "auth.ListApiKeys")
	defer span.End()

	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		span.SetAttributes(attribute.String("apikey.list.outcome", "invalid_user_id"))
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid_user_id"))
	}

	rows, listErr := s.Repo.ListAPIKeysByUser(ctx, userID)
	if listErr != nil {
		span.SetAttributes(attribute.String("apikey.list.outcome", "list_failed"))
		s.Logger.WarnContext(
			ctx, "apikey_list_failed",
			slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
			slog.String("error", listErr.Error()),
		)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("list: %w", listErr))
	}

	entries := make([]*authv1.ApiKeyEntry, 0, len(rows))
	for i := range rows {
		entries = append(entries, rowToProtoEntry(&rows[i]))
	}
	span.SetAttributes(
		attribute.String("apikey.list.outcome", "ok"),
		attribute.Int("apikey.list.count", len(entries)),
	)
	s.Logger.InfoContext(
		ctx, "apikey_list",
		slog.String("user_id_hash", oauth.HashClientIP(userID.String())),
		slog.Int("key_count", len(entries)),
	)
	return &authv1.ListApiKeysResponse{Keys: entries}, nil
}

// rowToProtoEntry projects a repository.ApiKeyRow onto an authv1.ApiKeyEntry
// proto message. Nullable pgtype values surface as proto3 `optional` field
// presence (nil pointer in the proto when DB column is NULL). The
// key_hash field is INTENTIONALLY ABSENT from the proto message (BR-2.5).
func rowToProtoEntry(r *repository.ApiKeyRow) *authv1.ApiKeyEntry {
	entry := &authv1.ApiKeyEntry{
		ApiKeyId:  r.ID.String(),
		Name:      r.Name,
		KeyPrefix: r.KeyPrefix,
		Scope:     string(r.Scope),
		CreatedAt: timestamppb.New(r.CreatedAt),
	}

	// current_month_cost_usd defaults to "0" if the NUMERIC column is null /
	// zero-valued — surface as the canonical "0" string per BR-2.10.
	if r.CurrentMonthCostUSD.Valid {
		entry.CurrentMonthCostUsd = numericToDecimalString(r.CurrentMonthCostUSD)
	} else {
		entry.CurrentMonthCostUsd = "0"
	}

	if r.MonthlyCostCapUSD.Valid {
		v := numericToDecimalString(r.MonthlyCostCapUSD)
		entry.MonthlyCostCapUsd = &v
	}
	if r.LastUsedAt.Valid {
		entry.LastUsedAt = timestamppb.New(r.LastUsedAt.Time)
	}
	if r.RevokedAt.Valid {
		entry.RevokedAt = timestamppb.New(r.RevokedAt.Time)
	}
	return entry
}
