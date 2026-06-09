// Package grpc implements billing-svc's BillingService.CheckBalance Connect-RPC
// (Story 7.1 AC3 / T3.4) — the sync, PG-authoritative balance read for console
// and reconciliation callers. The hot-path 402 gate does NOT use this RPC (it
// reads the fast Redis mirror); CheckBalance is for callers that need the
// durable truth.
package grpc

import (
	"context"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/billing-svc/internal/balance"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
)

// Server implements the BillingService Connect handler.
type Server struct {
	db     balance.Querier
	logger *slog.Logger
}

var _ billingv1connect.BillingServiceHandler = (*Server)(nil)

// NewServer builds the handler. logger may be nil (slog.Default()).
func NewServer(db balance.Querier, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{db: db, logger: logger}
}

// CheckBalance returns the PG-authoritative current_usd as a STRING-decimal
// (Q-Spec-4 — never a number) plus a sufficiency flag (current_usd > 0).
func (s *Server) CheckBalance(
	ctx context.Context,
	req *connect.Request[billingv1.CheckBalanceRequest],
) (*connect.Response[billingv1.CheckBalanceResponse], error) {
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidUserID)
	}

	res, err := balance.Read(ctx, s.db, userID)
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_check_balance_failed",
			slog.String("event", "billing_check_balance_failed"),
			slog.String("error", err.Error()),
		)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&billingv1.CheckBalanceResponse{
		CurrentUsd: res.CurrentUSD,
		Sufficient: res.Sufficient,
	}), nil
}

type constErr string

func (e constErr) Error() string { return string(e) }

const errInvalidUserID = constErr("user_id must not be empty")
