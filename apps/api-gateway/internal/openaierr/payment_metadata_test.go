package openaierr

import "testing"

// Story 7.3 — the four NEW §5.1.2 payment codes are registered in the runtime
// CodeMetadata mirror (Story-3.6 single-canonical-writer rule). Without this, a
// handler emitting one would hit Write's unknown-code defensive remap
// (500_internal_error) instead of the designed envelope (7.3-INT-018 / T3.4).
func Test7_3_PaymentCodesRegistered(t *testing.T) {
	want := map[string]struct {
		status    int
		errorType string
	}{
		"400_invalid_payment_request":      {400, "invalid_request_error"},
		"400_unsupported_payment_provider": {400, "invalid_request_error"},
		"402_payment_failed":               {402, "invalid_request_error"},
		"400_webhook_signature_invalid":    {400, "invalid_request_error"},
	}
	for code, exp := range want {
		meta, ok := CodeMetadata[code]
		if !ok {
			t.Errorf("%s missing from CodeMetadata (runtime mirror)", code)
			continue
		}
		if meta.HTTPStatus != exp.status {
			t.Errorf("%s HTTPStatus = %d, want %d", code, meta.HTTPStatus, exp.status)
		}
		if meta.ErrorType != exp.errorType {
			t.Errorf("%s ErrorType = %q, want %q", code, meta.ErrorType, exp.errorType)
		}
	}
}
