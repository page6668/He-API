// Package totp implements RFC 6238 Time-Based One-Time Password generation
// and validation, plus the `otpauth://` KeyURI helper Google Authenticator /
// Authy / 1Password / Microsoft Authenticator parse.
//
// Wright Round 1 Q3 ruling: SHA1 / 6 digits / 30s period — universal
// authenticator-app compatibility (RFC 6238 §3 security gap is negligible).
//
// Invariants (BR-2.4, BR-2.5, BR-5.5, BR-5.6):
//   - Secret entropy: 160 bits (20 bytes), crypto/rand.
//   - Code length: 6 digits (zero-padded ASCII).
//   - Period: 30s.
//   - Validation window: ±1 period (i.e., previous, current, next).
//   - Comparison: crypto/subtle.ConstantTimeCompare — defeats timing oracles.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"time"
)

// SecretBytes is the RFC 4226 §4 / RFC 6238 recommended secret length
// (160-bit). Matches Google Authenticator / Authy / Microsoft Authenticator
// expected input length.
const SecretBytes = 20

// Tunables — exported as constants so tests can reference them without
// magic numbers.
const (
	Digits = 6
	Period = 30 * time.Second
	// DefaultWindow is the ±N-period validation window. RFC 6238 §5.2
	// recommends ±1 — balances clock drift vs replay window.
	DefaultWindow = 1
	// Algorithm is the otpauth `algorithm=` URI param. Default SHA1 per
	// Architect Q3 (universal authenticator-app compat).
	Algorithm = "SHA1"
)

// Secret is a 20-byte newtype wrapping the raw HMAC key. The String() method
// emits RFC 4648 base32 (no padding) — the format authenticator apps expect.
type Secret []byte

// String returns base32-encoded secret (RFC 4648, no padding). Suitable for
// embedding in otpauth URIs and manual-entry fallback UI.
func (s Secret) String() string {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return enc.EncodeToString(s)
}

// GenerateSecret returns a fresh 20-byte secret from crypto/rand. BR-1.2 —
// NEVER math/rand. Returns the raw bytes; caller persists via KMS envelope
// encryption (BR-5.2).
func GenerateSecret() (Secret, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("totp: rand: %w", err)
	}
	return b, nil
}

// BuildOtpauthURI returns the otpauth://totp/ URI per KeyURI Format spec.
// The URI embeds the base32 secret + issuer + algorithm + digits + period
// — authenticator apps render a QR from this directly.
//
// Format: otpauth://totp/{issuer}:{accountName}?secret=...&issuer=...
//
//	&algorithm=SHA1&digits=6&period=30
//
// Per BR-1.3 the label is "{issuer}:{accountName}" (typically issuer="He-API"
// and accountName=user.email).
func BuildOtpauthURI(issuer, accountName string, secret Secret) string {
	label := url.PathEscape(issuer + ":" + accountName)
	q := url.Values{}
	q.Set("secret", secret.String())
	q.Set("issuer", issuer)
	q.Set("algorithm", Algorithm)
	q.Set("digits", strconv.Itoa(Digits))
	q.Set("period", strconv.Itoa(int(Period.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Generate produces the TOTP code at instant `t` for the supplied secret.
// Implements RFC 6238 §4: T = (unix_seconds / period); code = HOTP(secret, T).
//
// Returned string is exactly 6 ASCII digits, zero-padded ("042739").
func Generate(secret Secret, t time.Time) string {
	counter := uint64(t.Unix()) / uint64(Period.Seconds())
	return hotp(secret, counter, Digits)
}

// Validate returns true iff `code` matches the secret within ±window periods
// of `t`. Use window=DefaultWindow (1) for the standard ±30s allowance. The
// comparison is constant-time (crypto/subtle.ConstantTimeCompare) — BR-5.5.
//
// Validate iterates the full 2*window+1 range even when an early match is
// found, so timing observations cannot leak the matched window slot.
func Validate(secret Secret, code string, t time.Time, window int) bool {
	if len(code) != Digits {
		return false
	}
	counter := uint64(t.Unix()) / uint64(Period.Seconds())
	match := 0
	for i := -window; i <= window; i++ {
		c := int64(counter) + int64(i)
		if c < 0 {
			// Pre-epoch counters are nonsensical — skip but keep loop length.
			continue
		}
		expected := hotp(secret, uint64(c), Digits)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			match = 1
			// Do NOT break — preserve constant-time iteration.
		}
	}
	return match == 1
}

// hotp implements the HOTP truncation/dynamic-offset core (RFC 4226 §5.3).
func hotp(secret []byte, counter uint64, digits int) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)

	mac := hmac.New(func() hash.Hash { return sha1.New() }, secret)
	mac.Write(buf)
	sum := mac.Sum(nil)

	// Dynamic truncation: low-4 nibble of the last byte gives the offset.
	offset := int(sum[len(sum)-1] & 0x0F)
	bin := (uint32(sum[offset])&0x7F)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	v := bin % mod
	return fmt.Sprintf("%0*d", digits, v)
}
