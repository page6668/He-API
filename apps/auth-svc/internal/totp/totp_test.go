package totp

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Scenario: 2.4-UNIT-001 — GenerateSecret yields 20 bytes from crypto/rand.
func TestGenerateSecret_Length(t *testing.T) {
	t.Parallel()
	s, err := GenerateSecret()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if len(s) != SecretBytes {
		t.Fatalf("len=%d want %d", len(s), SecretBytes)
	}
}

// Scenario: 2.4-UNIT-002 — secret entropy: 100 generations should produce
// 100 distinct values (birthday collision odds vanishingly small for 160-bit).
func TestGenerateSecret_Distinct(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		s, err := GenerateSecret()
		if err != nil {
			t.Fatalf("gen %d: %v", i, err)
		}
		if _, dup := seen[string(s)]; dup {
			t.Fatalf("dup at i=%d", i)
		}
		seen[string(s)] = struct{}{}
	}
}

// Scenario: 2.4-UNIT-003 — secret.String() is RFC 4648 base32 (no padding).
func TestSecret_String_Base32(t *testing.T) {
	t.Parallel()
	s := Secret(strings.Repeat("\x00", SecretBytes))
	enc := s.String()
	if strings.Contains(enc, "=") {
		t.Fatalf("expected no padding, got %q", enc)
	}
	dec, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec) != SecretBytes {
		t.Fatalf("round-trip len=%d", len(dec))
	}
}

// Scenario: 2.4-UNIT-004 — RFC 6238 §5 test vectors (SHA1 secret).
// Spec vectors use 20-byte ASCII secret "12345678901234567890" and known T-times.
func TestGenerate_RFC6238Vectors(t *testing.T) {
	t.Parallel()
	secret := Secret("12345678901234567890")
	// (unix-seconds → expected 6-digit code) per RFC 6238 Appendix B (SHA1).
	vectors := []struct {
		t    int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, v := range vectors {
		got := Generate(secret, time.Unix(v.t, 0))
		if got != v.want {
			t.Errorf("t=%d: got %q want %q", v.t, got, v.want)
		}
	}
}

// Scenario: 2.4-UNIT-005 — Validate accepts current period.
func TestValidate_Current(t *testing.T) {
	t.Parallel()
	secret, _ := GenerateSecret()
	now := time.Now()
	code := Generate(secret, now)
	if !Validate(secret, code, now, DefaultWindow) {
		t.Fatalf("current code rejected")
	}
}

// Scenario: 2.4-UNIT-006 — Validate accepts code from t-1 and t+1 windows.
func TestValidate_PrevNextWindow(t *testing.T) {
	t.Parallel()
	secret, _ := GenerateSecret()
	now := time.Unix(1_700_000_000, 0)
	prev := Generate(secret, now.Add(-Period))
	next := Generate(secret, now.Add(Period))
	if !Validate(secret, prev, now, 1) {
		t.Fatalf("previous-window code rejected")
	}
	if !Validate(secret, next, now, 1) {
		t.Fatalf("next-window code rejected")
	}
}

// Scenario: 2.4-UNIT-007 — Validate rejects code from t-2 (outside window).
func TestValidate_OutsideWindowRejected(t *testing.T) {
	t.Parallel()
	secret, _ := GenerateSecret()
	now := time.Unix(1_700_000_000, 0)
	old := Generate(secret, now.Add(-2*Period))
	if Validate(secret, old, now, 1) {
		t.Fatalf("t-2 code should be rejected with window=1")
	}
}

// Scenario: 2.4-UNIT-008 — BuildOtpauthURI returns RFC 6238 KeyURI format.
func TestBuildOtpauthURI(t *testing.T) {
	t.Parallel()
	secret := Secret("12345678901234567890")
	uri := BuildOtpauthURI("He-API", "user@example.com", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("missing prefix: %q", uri)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("secret") != secret.String() {
		t.Errorf("secret param wrong: %q", q.Get("secret"))
	}
	if q.Get("issuer") != "He-API" {
		t.Errorf("issuer wrong: %q", q.Get("issuer"))
	}
	if q.Get("algorithm") != "SHA1" {
		t.Errorf("algorithm: %q", q.Get("algorithm"))
	}
	if q.Get("digits") != "6" {
		t.Errorf("digits: %q", q.Get("digits"))
	}
	if q.Get("period") != "30" {
		t.Errorf("period: %q", q.Get("period"))
	}
}

// Scenario: 2.4-UNIT-009 — Validate rejects wrong-length codes.
func TestValidate_LengthCheck(t *testing.T) {
	t.Parallel()
	secret, _ := GenerateSecret()
	now := time.Now()
	if Validate(secret, "12345", now, 1) {
		t.Errorf("5-digit accepted")
	}
	if Validate(secret, "1234567", now, 1) {
		t.Errorf("7-digit accepted")
	}
	if Validate(secret, "", now, 1) {
		t.Errorf("empty accepted")
	}
}
