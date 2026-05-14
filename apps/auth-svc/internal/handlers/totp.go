// Story 2.4 — TOTP 2FA shared helpers. The individual handlers live in
// totp_enroll.go (T1.2 — AC1), totp_challenge.go (T2.4 — AC2),
// totp_recovery.go (T3.2 — AC3), and totp_disable.go (T4.2 — AC4).
package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// Redis key helpers for AC1 pending-enrollment blob and AC2 JTI registry.
// data-models.md §4.3 backfill (Architect m-3 tracking item) registers these.
func enrollPendingKey(userID uuid.UUID) string {
	return "auth:2fa:enroll:" + userID.String()
}

func challengeJTIKey(jti string) string {
	sum := sha256.Sum256([]byte(jti))
	return "auth:2fa:challenge:" + hex.EncodeToString(sum[:])
}

// Tunable for AC1-AC4 (Story 2.4 BR-1.5, BR-2.1, BR-5.1 matrix).
const (
	enrollPendingTTL = 10 * time.Minute
	challengeJTITTL  = 5 * time.Minute

	// Rate-limit thresholds (BR-5.1).
	rl2FAEnrollInitLimit    = 3
	rl2FAEnrollInitWindow   = time.Hour
	rl2FAEnrollVerifyLimit  = 3
	rl2FAEnrollVerifyWindow = 15 * time.Minute
	rl2FAChallengeLimit     = 5
	rl2FAChallengeWindow    = 15 * time.Minute
	rl2FARecoveryLimit      = 3
	rl2FARecoveryWindow     = 15 * time.Minute
	rl2FADisableLimit       = 3
	rl2FADisableWindow      = time.Hour
)
