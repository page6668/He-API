// Story 5.4 — 5.4-UNIT-058..062 (GetCapNotificationContext gRPC handler:
// validation + happy path + NotFound + repo-error mapping).
package handlers_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

var capCtxColumns = []string{"email", "locale", "display_name", "name", "monthly_cost_cap_usd"}

func callCapCtx(t *testing.T, h *harness, apiKeyID string) (*authv1.GetCapNotificationContextResponse, error) {
	t.Helper()
	resp, err := h.srv.GetCapNotificationContext(context.Background(),
		connect.NewRequest(&authv1.GetCapNotificationContextRequest{ApiKeyId: apiKeyID}))
	if resp == nil {
		return nil, err
	}
	return resp.Msg, err
}

// UNIT-058: invalid UUID → InvalidArgument, no DB query.
func TestCapCtx_InvalidUUID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, err := callCapCtx(t, h, "not-a-uuid")
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", connect.CodeOf(err))
	}
}

// UNIT-059: happy path → all four fields mapped.
func TestCapCtx_HappyPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	h.mock.ExpectQuery(`SELECT u\.email, u\.locale, COALESCE\(u\.display_name, ''\), ak\.name`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(capCtxColumns).
			AddRow("alex@example.com", "ja", "Alex", "prod-key", "50.00"))

	resp, err := callCapCtx(t, h, id.String())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.GetUserEmail() != "alex@example.com" || resp.GetUserLocale() != "ja" ||
		resp.GetUserDisplayName() != "Alex" || resp.GetKeyName() != "prod-key" ||
		resp.GetKeyMonthlyCostCapUsd() != "50.00" {
		t.Fatalf("bad mapping: %+v", resp)
	}
}

// UNIT-060: empty display_name (COALESCE'd NULL) passes through as "".
func TestCapCtx_EmptyDisplayName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	h.mock.ExpectQuery(`SELECT u\.email`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(capCtxColumns).
			AddRow("alex@example.com", "en", "", "k", "10.00"))

	resp, err := callCapCtx(t, h, id.String())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.GetUserDisplayName() != "" {
		t.Fatalf("display_name=%q want empty", resp.GetUserDisplayName())
	}
}

// UNIT-061: zero rows → NotFound.
func TestCapCtx_NotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	// Empty row set → QueryRow.Scan yields pgx.ErrNoRows → ErrAPIKeyNotFound.
	h.mock.ExpectQuery(`SELECT u\.email`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows(capCtxColumns))

	_, err := callCapCtx(t, h, id.String())
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code=%v want NotFound", connect.CodeOf(err))
	}
}

// UNIT-062: arbitrary DB error → Internal.
func TestCapCtx_DBError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := uuid.New()
	h.mock.ExpectQuery(`SELECT u\.email`).
		WithArgs(id).
		WillReturnError(errors.New("connection reset"))

	_, err := callCapCtx(t, h, id.String())
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code=%v want Internal", connect.CodeOf(err))
	}
}
