package openaierr

import "testing"

// 7.2-UNIT-013 P0 [M-1] — 400_unsupported_currency is registered in the runtime
// CodeMetadata mirror as an invalid_request_error / 400 (Story-3.6 single-
// canonical-writer rule). Without this, any caller emitting the code would hit
// Write's unknown-code defensive remap (500_internal_error) instead of the
// designed 400 envelope — a contract regression. See INT-013/INT-015.
func Test7_2_UNIT013_UnsupportedCurrencyRegistered(t *testing.T) {
	meta, ok := CodeMetadata["400_unsupported_currency"]
	if !ok {
		t.Fatal("400_unsupported_currency missing from CodeMetadata (runtime mirror)")
	}
	if meta.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", meta.HTTPStatus)
	}
	if meta.ErrorType != "invalid_request_error" {
		t.Errorf("ErrorType = %q, want invalid_request_error", meta.ErrorType)
	}
}
