package fxrate

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	v, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return v
}

// 7.2-UNIT-001 P0 — convert(12.3400, 7.21000000) -> "88.97" (12.34 × 7.21 =
// 88.9714 → HALF-UP 2dp). Canonical golden vector (BR-B-6).
func TestConvert_GoldenVector(t *testing.T) {
	got := Convert(d(t, "12.3400"), d(t, "7.21000000"), DisplayScale).StringFixed(DisplayScale)
	if got != "88.97" {
		t.Fatalf("convert = %q, want \"88.97\"", got)
	}
}

// 7.2-UNIT-002 / BLIND-BOUNDARY-003 P0 — HALF-UP at the .xx5 boundary rounds UP
// (never half-even, never down). Q-ROUND.
func TestConvert_HalfUpBoundary(t *testing.T) {
	cases := []struct{ usd, rate, want string }{
		// 0.5 × 1.25 = 0.625 → 0.63 (round up at .5)
		{"0.5000", "1.25000000", "0.63"},
		// 2.5 × 1.0 = 2.5 → at 0dp would be 3; at 2dp it's exact 2.50.
		{"0.1250", "1.00000000", "0.13"}, // 0.125 → 0.13 (half away from zero, UP)
		{"0.1350", "1.00000000", "0.14"}, // 0.135 → 0.14
	}
	for _, c := range cases {
		got := Convert(d(t, c.usd), d(t, c.rate), DisplayScale).StringFixed(DisplayScale)
		if got != c.want {
			t.Errorf("convert(%s,%s) = %q, want %q (HALF-UP)", c.usd, c.rate, got, c.want)
		}
	}
}

// 7.2-UNIT-003 / BLIND-BOUNDARY-001 P0 — zero balance convert -> "0.00".
func TestConvert_Zero(t *testing.T) {
	got := Convert(d(t, "0.0000"), d(t, "7.21000000"), DisplayScale).StringFixed(DisplayScale)
	if got != "0.00" {
		t.Fatalf("convert(0) = %q, want \"0.00\"", got)
	}
}

// 7.2-UNIT-004 / BLIND-BOUNDARY-002 P1 — large balance × 8dp rate -> exact, no
// overflow / precision loss (Decimal is arbitrary-precision).
func TestConvert_LargeMagnitude(t *testing.T) {
	// 1_000_000.0000 × 7.21345678 = 7_213_456.78
	got := Convert(d(t, "1000000.0000"), d(t, "7.21345678"), DisplayScale).StringFixed(DisplayScale)
	if got != "7213456.78" {
		t.Fatalf("large convert = %q, want \"7213456.78\"", got)
	}
}

// 7.2-UNIT-005 P0 [M-1] — float-slip vector: an input that float64 arithmetic
// mis-rounds is byte-exact under shopspring/decimal HALF-UP. Decimal-not-float
// guard (7.1 M-1 cascade).
func TestConvert_DecimalNotFloat(t *testing.T) {
	// 0.10 + 0.20 in float64 is 0.30000000000000004. As a rate-multiply vector:
	// 70.7000 × 1.10000000 = 77.77 exactly under Decimal; float64 would yield
	// 77.77000000000001 and could mis-round at the boundary.
	got := Convert(d(t, "70.7000"), d(t, "1.10000000"), DisplayScale).StringFixed(DisplayScale)
	if got != "77.77" {
		t.Fatalf("float-slip vector = %q, want \"77.77\"", got)
	}
	// 0.1 × 3 = 0.3 (float64: 0.30000000000000004 → could show 0.30 but the
	// classic slip is at higher scales). Assert Decimal exactness.
	if Convert(d(t, "0.1000"), d(t, "3.00000000"), 4).String() != "0.3" {
		t.Fatalf("0.1×3 not exact under Decimal")
	}
}

// 7.2-UNIT-006 / BLIND-BOUNDARY-005 P0 — full 8dp NUMERIC(18,8) rate applied
// BEFORE rounding the display to 2dp (precision order, Q-ROUND).
func TestConvert_PrecisionOrder(t *testing.T) {
	// 10.0000 × 7.21345678 = 72.1345678 → HALF-UP 2dp = 72.13.
	got := Convert(d(t, "10.0000"), d(t, "7.21345678"), DisplayScale).StringFixed(DisplayScale)
	if got != "72.13" {
		t.Fatalf("precision-order = %q, want \"72.13\"", got)
	}
}

// 7.2-UNIT-007 P1 — USD-default identity: rate=1.00000000 ⇒ display == the SoT
// at 4dp (echoes current_usd, NOT 2dp-rounded). Byte-compat with 7.1 (BR-B-7).
func TestConvert_USDIdentity4dp(t *testing.T) {
	got := Convert(d(t, "12.3400"), d(t, "1.00000000"), USDScale).StringFixed(USDScale)
	if got != "12.3400" {
		t.Fatalf("USD identity = %q, want \"12.3400\" (4dp echo, not 2dp)", got)
	}
}

// 7.2-UNIT-008 P1 — purity: same (usd, rate, places) -> identical Decimal; no
// I/O, no clock.
func TestConvert_Pure(t *testing.T) {
	a := Convert(d(t, "3.3300"), d(t, "7.21000000"), DisplayScale)
	b := Convert(d(t, "3.3300"), d(t, "7.21000000"), DisplayScale)
	if !a.Equal(b) {
		t.Fatalf("not pure: %s != %s", a, b)
	}
}

// 7.2-UNIT-009 P2 — bounded-negative balance (7.1 E2E-003 race edge) -> signed
// display. convert(-0.5000, 7.21) -> "-3.61" (half away from zero).
func TestConvert_NegativeBound(t *testing.T) {
	got := Convert(d(t, "-0.5000"), d(t, "7.21000000"), DisplayScale).StringFixed(DisplayScale)
	if got != "-3.61" {
		t.Fatalf("negative convert = %q, want \"-3.61\"", got)
	}
}
