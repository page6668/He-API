// Metrics package tests — UNIT-185 / UNIT-186.
//
// UNIT-185 pins the counter NAMES + label keys. The names are load-bearing:
// the Grafana dashboard in T4.7 (BR-4.9) queries them by literal name. If
// any counter is renamed without updating the dashboard, observability
// silently breaks. These tests fail on rename so the contract stays loud.
//
// UNIT-186 verifies the Inc* methods emit the right counter values + label
// attributes by running them against an OTel SDK manual-reader meter
// provider and inspecting the collected metric stream.

package metrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// --- UNIT-185: counter names / label keys are static constants ---

func TestCounterNames_Pinned(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{CounterSignupTotal, "auth_signup_total"},
		{CounterSigninTotal, "auth_signin_total"},
		{CounterVerifyEmailTotal, "auth_verify_email_total"},
		{CounterAccountLockedTotal, "auth_account_locked_total"},
		{CounterRateLimitTriggeredTotal, "auth_rate_limit_triggered_total"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("counter name drift: got %q, want %q (UNIT-185)", c.got, c.want)
		}
	}
}

func TestLabelKeys_Pinned(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{LabelResult, "result"},
		{LabelErrorCode, "error_code"},
		{LabelEndpoint, "endpoint"},
		{LabelKeyType, "key_type"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("label key drift: got %q, want %q (UNIT-185)", c.got, c.want)
		}
	}
}

// --- UNIT-186: Inc* methods emit the right value + labels ---

// withManualReader installs a fresh meter provider with a manual reader as
// the global OTel meter provider for the duration of one test. Returns
// the reader so the test can collect the recorded metrics.
func withManualReader(t *testing.T) *metric.ManualReader {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return reader
}

// collectInt64Sum returns the int64 sum data point for a named counter in
// the reader's collected metrics, or fails the test if not present.
func collectInt64Sum(t *testing.T, reader *metric.ManualReader, name string) []metricdata.DataPoint[int64] {
	t.Helper()
	rm := &metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("counter %q is not Sum[int64], got %T", name, m.Data)
			}
			return sum.DataPoints
		}
	}
	t.Fatalf("counter %q not collected (UNIT-186)", name)
	return nil
}

// findDataPoint looks for a data point whose attribute set matches the
// supplied key/value pairs exactly. Order-independent.
func findDataPoint(dps []metricdata.DataPoint[int64], wantAttrs map[string]string) (metricdata.DataPoint[int64], bool) {
	for _, dp := range dps {
		if !attrsEqual(dp.Attributes, wantAttrs) {
			continue
		}
		return dp, true
	}
	return metricdata.DataPoint[int64]{}, false
}

func attrsEqual(got attribute.Set, want map[string]string) bool {
	if got.Len() != len(want) {
		return false
	}
	for k, v := range want {
		val, ok := got.Value(attribute.Key(k))
		if !ok || val.AsString() != v {
			return false
		}
	}
	return true
}

func TestIncSignup_EmitsResultLabel(t *testing.T) {
	reader := withManualReader(t)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	c.IncSignup(ctx, SignupResultSuccess)
	c.IncSignup(ctx, SignupResultDuplicate)
	c.IncSignup(ctx, SignupResultFailure)
	c.IncSignup(ctx, SignupResultSuccess) // a second success

	dps := collectInt64Sum(t, reader, CounterSignupTotal)
	cases := []struct {
		result string
		want   int64
	}{
		{"success", 2},
		{"duplicate", 1},
		{"failure", 1},
	}
	for _, tc := range cases {
		dp, ok := findDataPoint(dps, map[string]string{LabelResult: tc.result})
		if !ok {
			t.Errorf("signup result=%q data point missing", tc.result)
			continue
		}
		if dp.Value != tc.want {
			t.Errorf("signup result=%q value: got %d, want %d", tc.result, dp.Value, tc.want)
		}
	}
}

func TestIncSignin_EmitsResultAndErrorCode(t *testing.T) {
	reader := withManualReader(t)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	c.IncSignin(ctx, SigninResultSuccess, "")
	c.IncSignin(ctx, SigninResultFailure, "401_invalid_credentials")
	c.IncSignin(ctx, SigninResultFailure, "401_invalid_credentials")
	c.IncSignin(ctx, SigninResultFailure, "423_account_locked")

	dps := collectInt64Sum(t, reader, CounterSigninTotal)
	cases := []struct {
		result, errorCode string
		want              int64
	}{
		{"success", "", 1},
		{"failure", "401_invalid_credentials", 2},
		{"failure", "423_account_locked", 1},
	}
	for _, tc := range cases {
		dp, ok := findDataPoint(dps, map[string]string{
			LabelResult:    tc.result,
			LabelErrorCode: tc.errorCode,
		})
		if !ok {
			t.Errorf("signin result=%q error_code=%q data point missing", tc.result, tc.errorCode)
			continue
		}
		if dp.Value != tc.want {
			t.Errorf("signin result=%q error_code=%q value: got %d, want %d", tc.result, tc.errorCode, dp.Value, tc.want)
		}
	}
}

func TestIncVerifyEmail_EmitsResultLabel(t *testing.T) {
	reader := withManualReader(t)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	c.IncVerifyEmail(ctx, VerifyEmailResultSuccess)
	c.IncVerifyEmail(ctx, VerifyEmailResultExpired)
	c.IncVerifyEmail(ctx, VerifyEmailResultBruteForce)
	c.IncVerifyEmail(ctx, VerifyEmailResultAlreadyVerified)
	c.IncVerifyEmail(ctx, VerifyEmailResultInvalidFormat)

	dps := collectInt64Sum(t, reader, CounterVerifyEmailTotal)
	wants := []string{"success", "expired", "brute_force", "already_verified", "invalid_format"}
	for _, r := range wants {
		dp, ok := findDataPoint(dps, map[string]string{LabelResult: r})
		if !ok {
			t.Errorf("verify_email result=%q data point missing", r)
			continue
		}
		if dp.Value != 1 {
			t.Errorf("verify_email result=%q value: got %d, want 1", r, dp.Value)
		}
	}
}

func TestIncAccountLocked_NoLabels(t *testing.T) {
	reader := withManualReader(t)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	c.IncAccountLocked(ctx)
	c.IncAccountLocked(ctx)
	c.IncAccountLocked(ctx)

	dps := collectInt64Sum(t, reader, CounterAccountLockedTotal)
	if len(dps) != 1 {
		t.Fatalf("account_locked data points: got %d, want 1 (no labels)", len(dps))
	}
	if dps[0].Attributes.Len() != 0 {
		t.Errorf("account_locked attrs: got %d, want 0 (no labels by design)", dps[0].Attributes.Len())
	}
	if dps[0].Value != 3 {
		t.Errorf("account_locked value: got %d, want 3", dps[0].Value)
	}
}

func TestIncRateLimitTriggered_EmitsEndpointAndKeyType(t *testing.T) {
	reader := withManualReader(t)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	c.IncRateLimitTriggered(ctx, RateLimitEndpointSignup, RateLimitKeyTypeIP)
	c.IncRateLimitTriggered(ctx, RateLimitEndpointSignin, RateLimitKeyTypeIP)
	c.IncRateLimitTriggered(ctx, RateLimitEndpointSignin, RateLimitKeyTypeEmail)
	c.IncRateLimitTriggered(ctx, RateLimitEndpointResend, RateLimitKeyTypeIP)
	c.IncRateLimitTriggered(ctx, RateLimitEndpointResend, RateLimitKeyTypeEmail)

	dps := collectInt64Sum(t, reader, CounterRateLimitTriggeredTotal)
	cases := []struct {
		endpoint, keyType string
	}{
		{"signup", "ip"},
		{"signin", "ip"},
		{"signin", "email"},
		{"resend", "ip"},
		{"resend", "email"},
	}
	for _, tc := range cases {
		dp, ok := findDataPoint(dps, map[string]string{
			LabelEndpoint: tc.endpoint,
			LabelKeyType:  tc.keyType,
		})
		if !ok {
			t.Errorf("rate_limit endpoint=%q key_type=%q data point missing", tc.endpoint, tc.keyType)
			continue
		}
		if dp.Value != 1 {
			t.Errorf("rate_limit endpoint=%q key_type=%q value: got %d, want 1", tc.endpoint, tc.keyType, dp.Value)
		}
	}
}

// --- Nil-safety: methods on a nil *Counters MUST NOT panic. The
// AuthServer's tests pass nil for Metrics when they don't care about
// counters; the handler call sites would dereference into Inc* and that
// path must be safe.

func TestNilCounters_DoNotPanic(t *testing.T) {
	var c *Counters
	ctx := context.Background()
	c.IncSignup(ctx, SignupResultSuccess)
	c.IncSignin(ctx, SigninResultSuccess, "")
	c.IncVerifyEmail(ctx, VerifyEmailResultSuccess)
	c.IncAccountLocked(ctx)
	c.IncRateLimitTriggered(ctx, RateLimitEndpointSignup, RateLimitKeyTypeIP)
}
