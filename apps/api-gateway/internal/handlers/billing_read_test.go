package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func billingReq(t *testing.T, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	return req.WithContext(middleware.BearerWithUserID(req.Context(), "u-1"))
}

// 7.1-INT-021 / UNIT-019 — GET /v1/balance returns current_usd as a STRING
// decimal ("12.3400"), read from PG (authoritative). The money field must
// decode as a JSON string, never a number.
func TestBalance_StringMoneyFromPG(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	// Money MUST be a JSON string, not a number.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if string(raw["current_usd"]) != `"12.3400"` {
		t.Fatalf("current_usd = %s, want \"12.3400\" (string-decimal)", raw["current_usd"])
	}
}

// 7.1-INT-022 — absent balances row → 200 {"current_usd":"0.0000",...}.
func TestBalance_AbsentRowZero(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-1").
		WillReturnError(pgx.ErrNoRows)

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance"))

	var resp struct {
		CurrentUSD string `json:"current_usd"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || resp.CurrentUSD != "0.0000" {
		t.Fatalf("absent row: status=%d current_usd=%q, want 200 / 0.0000", rr.Code, resp.CurrentUSD)
	}
}

// 7.1-INT-023 — GET /v1/usage aggregates totals + per-model breakdown.
func TestUsage_AggregateAndByModel(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.usage_ledger").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum", "count", "tokens"}).
			AddRow("4.5600", 132, int64(845000)))
	mock.ExpectQuery("GROUP BY model").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"model", "sum", "count", "tokens"}).
			AddRow("deepseek-v3", "1.5600", 50, int64(300000)).
			AddRow("qwen-max", "3.0000", 82, int64(545000)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Usage(rr, billingReq(t, "/v1/usage"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Period        string `json:"period"`
		TotalCostUSD  string `json:"total_cost_usd"`
		TotalRequests int    `json:"total_requests"`
		TotalTokens   int64  `json:"total_tokens"`
		ByModel       []struct {
			Model   string `json:"model"`
			CostUSD string `json:"cost_usd"`
		} `json:"by_model"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.TotalCostUSD != "4.5600" || resp.TotalRequests != 132 || resp.TotalTokens != 845000 {
		t.Fatalf("totals wrong: %+v", resp)
	}
	if len(resp.ByModel) != 2 || resp.ByModel[0].CostUSD != "1.5600" {
		t.Fatalf("by_model wrong: %+v", resp.ByModel)
	}
}

// 7.1-INT-024 — GET /v1/usage with no rows → zero-aggregate.
func TestUsage_ZeroAggregate(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.usage_ledger").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum", "count", "tokens"}).
			AddRow("0", 0, int64(0)))
	mock.ExpectQuery("GROUP BY model").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"model", "sum", "count", "tokens"}))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Usage(rr, billingReq(t, "/v1/usage"))

	var resp struct {
		TotalCostUSD  string `json:"total_cost_usd"`
		TotalRequests int    `json:"total_requests"`
		ByModel       []any  `json:"by_model"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || resp.TotalCostUSD != "0.0000" || resp.TotalRequests != 0 || len(resp.ByModel) != 0 {
		t.Fatalf("zero aggregate wrong: status=%d %+v", rr.Code, resp)
	}
}
