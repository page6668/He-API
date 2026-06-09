// Package fx is billing-svc's FX-rate refresh seam (Story 7.2 AC2). It is the
// ONLY component that talks to the external FX provider — and it is touched ONLY
// by the daily `fx-refresh` cron, NEVER on a user read hot path (BR-C-5). The
// gateway converts display currency from a PG snapshot (api-gateway/internal/
// fxrate); this package's job is to keep that PG table fresh.
//
// The posture is STALE-SERVE (BR-C-3 / Q-FXFAIL): on any provider failure
// (timeout / non-2xx / malformed / non-positive rate) the cron inserts NOTHING,
// the last-known fx_rates row stays active, an alert metric fires, and the cron
// exits 0 (retries next day). A wrong (zero/null) rate is NEVER persisted.
//
// The fetched rate value MAY be logged (public market data); the provider
// base-URL/credential is a NEW secret and is NEVER logged (BR-C-7) — errors here
// are deliberately generic so a key embedded in the URL cannot leak.
package fx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// BaseCurrency / QuoteCurrency — MVP fetches exactly USD→CNY (BR-C-8). The
// FxProvider seam + fx_rates (base,quote) key generalise to more pairs without a
// schema change.
const (
	BaseCurrency  = "USD"
	QuoteCurrency = "CNY"
)

// DefaultTimeout bounds a single provider fetch (Q-FXSRC ADD — stale-serve
// correctness depends on a bounded fetch; UNIT-018).
const DefaultTimeout = 10 * time.Second

// FxProvider returns the latest USD→CNY rate. Implementations: HTTPProvider
// (real upstream) and ManualProvider (FX_MANUAL_USD_CNY dev/CI/air-gapped).
type FxProvider interface {
	Get(ctx context.Context) (decimal.Decimal, error)
}

// ErrMalformed marks a provider response that parsed at the HTTP layer but did
// not carry a usable rate (missing field / wrong type) — a stale-serve trigger.
var ErrMalformed = errors.New("fx: malformed provider response")

// HTTPProvider fetches a USD-base rates document and reads the quote rate. It
// targets the common free "USD-base rates" JSON shape:
//
//	{"rates": {"CNY": 7.21, ...}}   (open.er-api.com / exchangerate.host style)
//
// The rate is decoded as a json.Number and parsed with decimal.NewFromString —
// NEVER float64 (the money path is Decimal end-to-end, M-1 cascade).
type HTTPProvider struct {
	client  *http.Client
	url     string // SECRET (may embed a key) — never logged, never in an error
	quote   string
	timeout time.Duration
}

// NewHTTPProvider builds an HTTPProvider. url is the secret base-URL (env-
// injected). timeout <= 0 → DefaultTimeout.
func NewHTTPProvider(url string, timeout time.Duration) *HTTPProvider {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &HTTPProvider{
		client:  &http.Client{Timeout: timeout},
		url:     url,
		quote:   QuoteCurrency,
		timeout: timeout,
	}
}

// ratesEnvelope is the minimal shape we read. json.Number preserves the exact
// literal so the rate never round-trips through float64.
type ratesEnvelope struct {
	Rates map[string]json.Number `json:"rates"`
}

// Get fetches USD→CNY. It applies a HARD context deadline (the provider's
// timeout) so a hung upstream cannot stall the cron (UNIT-018 / BLIND-RESOURCE-
// 001). Errors are generic — they never embed the secret URL (BR-C-7).
func (p *HTTPProvider) Get(ctx context.Context) (decimal.Decimal, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return decimal.Zero, errors.New("fx: build request failed")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Wrap WITHOUT the URL (it may carry a key); preserve the cause class
		// (timeout / conn refused) for the slog without leaking the secret.
		return decimal.Zero, fmt.Errorf("fx: provider request failed: %w", contextCause(ctx, err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // drain for connection reuse
		_ = resp.Body.Close()                 // RESOURCE: always released
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decimal.Zero, fmt.Errorf("fx: provider returned status %d", resp.StatusCode)
	}

	var env ratesEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return decimal.Zero, ErrMalformed
	}
	raw, ok := env.Rates[p.quote]
	if !ok || raw.String() == "" {
		return decimal.Zero, ErrMalformed
	}
	rate, err := decimal.NewFromString(raw.String())
	if err != nil {
		return decimal.Zero, ErrMalformed
	}
	return rate, nil
}

// contextCause surfaces a context deadline as the error so a bounded-fetch
// timeout is unambiguous in the slog (without leaking the URL).
func contextCause(ctx context.Context, err error) error {
	if cerr := ctx.Err(); errors.Is(cerr, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}
