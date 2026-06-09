package fx

import (
	"context"
	"errors"
	"strings"

	"github.com/shopspring/decimal"
)

// ManualProvider returns a fixed USD→CNY rate from the FX_MANUAL_USD_CNY env
// override (Q-FXSRC). It bypasses the HTTP provider for dev / CI / air-gapped
// runs — deterministic, no network.
type ManualProvider struct {
	rate decimal.Decimal
}

// NewManualProvider parses the override string. It FAILS FAST (Data-validation
// row / UNIT-017): an unparseable or non-positive value is a boot error, never a
// silently-served zero rate.
func NewManualProvider(raw string) (*ManualProvider, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("fx: FX_MANUAL_USD_CNY is empty")
	}
	rate, err := decimal.NewFromString(s)
	if err != nil {
		return nil, errors.New("fx: FX_MANUAL_USD_CNY is not a valid decimal")
	}
	if !rate.IsPositive() {
		return nil, errors.New("fx: FX_MANUAL_USD_CNY must be > 0")
	}
	return &ManualProvider{rate: rate}, nil
}

// Get returns the fixed override rate.
func (m *ManualProvider) Get(ctx context.Context) (decimal.Decimal, error) {
	return m.rate, nil
}

// ProviderFromEnv selects the FxProvider for a run: the FX_MANUAL_USD_CNY
// override when set (dev/CI), otherwise the HTTP provider over the secret base
// URL (HE_API_FX_PROVIDER_URL). Returns an error when neither is configured, or
// when the override is set-but-invalid (fail-fast, UNIT-017).
func ProviderFromEnv(getenv func(string) string) (FxProvider, error) {
	if manual := strings.TrimSpace(getenv("FX_MANUAL_USD_CNY")); manual != "" {
		return NewManualProvider(manual)
	}
	url := strings.TrimSpace(getenv("HE_API_FX_PROVIDER_URL"))
	if url == "" {
		return nil, errors.New("fx: no provider configured (set FX_MANUAL_USD_CNY or HE_API_FX_PROVIDER_URL)")
	}
	return NewHTTPProvider(url, DefaultTimeout), nil
}
