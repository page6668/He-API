package heapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// BalanceResponse is the He-API GET /v1/balance payload (currency-aware,
// Story 7.2). All monetary fields are string-decimals. Unknown gateway fields
// are tolerated (ignored) by encoding/json, keeping this thin convenience type
// forward-compatible.
type BalanceResponse struct {
	CurrentUSD       string `json:"current_usd"`
	Currency         string `json:"currency"`
	CurrentDisplay   string `json:"current_display"`
	TotalCostDisplay string `json:"total_cost_display"`
	FxRate           string `json:"fx_rate"`
	FxAsOf           string `json:"fx_as_of"`
	FxDegraded       bool   `json:"fx_degraded,omitempty"`
}

// UsageResponse is the He-API GET /v1/usage payload (this month, currency-aware
// display fields from Story 7.2). Per OQ5 the SDK never reads a per-request USD
// cost header; consumption is read here from the gateway's authoritative ledger.
type UsageResponse struct {
	Object           string `json:"object"`
	Currency         string `json:"currency"`
	TotalCostDisplay string `json:"total_cost_display"`
	FxRate           string `json:"fx_rate"`
	FxAsOf           string `json:"fx_as_of"`
	FxDegraded       bool   `json:"fx_degraded,omitempty"`
}

// Balance reads the caller's He-API balance via GET /v1/balance using openai-go's
// escape hatch (client.Get), so it inherits the same base URL, auth, retries and
// options as every other call. Because NewClient returns the upstream
// openai.Client (which has no place to hang custom methods), the He-specific
// endpoints are package-level functions taking the client (R-OQ-10.4-4).
//
// currency is forwarded verbatim as ?currency=<currency> when non-empty; the SDK
// does NOT validate it — the gateway is the single validator and returns
// 400_unsupported_currency for anything outside {usd, rmb} (BOUNDARY-005). An
// empty currency omits the query parameter, letting the gateway apply its USD
// default.
func Balance(ctx context.Context, client openai.Client, currency string, opts ...option.RequestOption) (*BalanceResponse, error) {
	var res BalanceResponse
	if err := client.Get(ctx, "balance", nil, &res, withCurrency(currency, opts)...); err != nil {
		return nil, err
	}
	return &res, nil
}

// Usage reads the caller's current-month usage via GET /v1/usage. See Balance for
// the currency-forwarding contract.
func Usage(ctx context.Context, client openai.Client, currency string, opts ...option.RequestOption) (*UsageResponse, error) {
	var res UsageResponse
	if err := client.Get(ctx, "usage", nil, &res, withCurrency(currency, opts)...); err != nil {
		return nil, err
	}
	return &res, nil
}

// withCurrency prepends a ?currency= query option when currency is non-empty,
// preserving caller-supplied opts (which win, as they are applied later).
func withCurrency(currency string, opts []option.RequestOption) []option.RequestOption {
	if currency == "" {
		return opts
	}
	return append([]option.RequestOption{option.WithQuery("currency", currency)}, opts...)
}

// HeRequestID extracts the He-API correlation id (he_request_id, format
// ^req_[0-9a-f]{12}$) from an error returned by any SDK call. It returns "" when
// err is not an *openai.Error or carries no he_request_id. The id is a He-API
// extension field on the error envelope; openai-go surfaces the raw envelope via
// (*openai.Error).RawJSON(), which this helper parses (tolerating either the
// inner error object or the full {"error":{...}} envelope).
func HeRequestID(err error) string {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return ""
	}
	var probe struct {
		HeRequestID string `json:"he_request_id"`
		Error       struct {
			HeRequestID string `json:"he_request_id"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(apiErr.RawJSON()), &probe)
	if probe.HeRequestID != "" {
		return probe.HeRequestID
	}
	return probe.Error.HeRequestID
}
