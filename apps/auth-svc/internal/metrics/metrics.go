// Package metrics registers the OTel counters for the auth surface
// (BR-4.8 + Story 2.2 T4.6 + UNIT-185).
//
// Five counter shapes, exactly per BR-4.8:
//
//   auth_signup_total{result}
//   auth_signin_total{result, error_code}
//   auth_verify_email_total{result}
//   auth_account_locked_total
//   auth_rate_limit_triggered_total{endpoint, key_type}
//
// Counter NAMES are load-bearing — they drive the Grafana dashboard +
// Alertmanager rules in Story 2.2 T4.7 (BR-4.9). Renaming any of these
// without updating the dashboard breaks observability silently. The
// constants below pin the names; tests verify them.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MeterName identifies the auth-svc meter instance.
const MeterName = "github.com/he-api/he-api/apps/auth-svc"

// Counter name constants. Static so callers + tests reference one
// source. Any drift between counter registration + Grafana dashboards
// would be silent — the constants make the contract greppable.
const (
	CounterSignupTotal             = "auth_signup_total"
	CounterSigninTotal             = "auth_signin_total"
	CounterVerifyEmailTotal        = "auth_verify_email_total"
	CounterAccountLockedTotal      = "auth_account_locked_total"
	CounterRateLimitTriggeredTotal = "auth_rate_limit_triggered_total"

	// Story 2.4 — 5 new 2FA counters per T5.4. Dashboards land in
	// Story 2.5 / observability epic; the counters register here so
	// instrumentation call sites can land BEFORE the panels.
	Counter2FAEnrollTotal       = "auth_2fa_enroll_total"
	Counter2FAChallengeTotal    = "auth_2fa_challenge_total"
	Counter2FARecoveryUsedTotal = "auth_2fa_recovery_used_total"
	Counter2FADisabledTotal     = "auth_2fa_disabled_total"
	Counter2FALockedTotal       = "auth_2fa_locked_total"
)

// Label keys per BR-4.8. Cardinality is bounded — `result` is a small
// closed set; `error_code` is the 21 canonical status codes; `endpoint`
// is the 5 ratelimit key types. No high-cardinality fields (email, IP)
// are labels — that would explode Prometheus storage.
const (
	LabelResult    = "result"
	LabelErrorCode = "error_code"
	LabelEndpoint  = "endpoint"
	LabelKeyType   = "key_type"
)

// Counters is the bundle of registered Int64Counter instruments. The
// AuthServer holds one instance and call sites increment via methods so
// the OTel attribute boilerplate stays out of handler code.
type Counters struct {
	signup             metric.Int64Counter
	signin             metric.Int64Counter
	verifyEmail        metric.Int64Counter
	accountLocked      metric.Int64Counter
	rateLimitTriggered metric.Int64Counter
	// Story 2.4 — 5 new 2FA counters.
	twofaEnroll       metric.Int64Counter
	twofaChallenge    metric.Int64Counter
	twofaRecoveryUsed metric.Int64Counter
	twofaDisabled     metric.Int64Counter
	twofaLocked       metric.Int64Counter
}

// New registers the 5 counters on the global OTel meter provider. Any
// error registering a counter is fatal — observability dashboards
// would silently lose data. cmd/server should panic on error so the
// deployment fails loudly.
func New() (*Counters, error) {
	meter := otel.GetMeterProvider().Meter(MeterName)

	signup, err := meter.Int64Counter(CounterSignupTotal,
		metric.WithDescription("auth-svc signup attempts (result=success|failure|duplicate)"),
	)
	if err != nil {
		return nil, err
	}
	signin, err := meter.Int64Counter(CounterSigninTotal,
		metric.WithDescription("auth-svc signin attempts (result=success|failure, error_code=NNN_xxx)"),
	)
	if err != nil {
		return nil, err
	}
	verifyEmail, err := meter.Int64Counter(CounterVerifyEmailTotal,
		metric.WithDescription("auth-svc verify-email attempts (result=success|expired|brute_force|already_verified)"),
	)
	if err != nil {
		return nil, err
	}
	accountLocked, err := meter.Int64Counter(CounterAccountLockedTotal,
		metric.WithDescription("auth-svc soft-lock activations (5th wrong-password threshold)"),
	)
	if err != nil {
		return nil, err
	}
	rateLimitTriggered, err := meter.Int64Counter(CounterRateLimitTriggeredTotal,
		metric.WithDescription("auth-svc rate-limit trips (endpoint=signup|signin|resend, key_type=ip|email)"),
	)
	if err != nil {
		return nil, err
	}

	// Story 2.4 — 2FA counters.
	twofaEnroll, err := meter.Int64Counter(Counter2FAEnrollTotal,
		metric.WithDescription("auth-svc 2FA enrollments (result=success|failure)"),
	)
	if err != nil {
		return nil, err
	}
	twofaChallenge, err := meter.Int64Counter(Counter2FAChallengeTotal,
		metric.WithDescription("auth-svc 2FA challenge attempts (result=success|failure, factor=totp|recovery)"),
	)
	if err != nil {
		return nil, err
	}
	twofaRecoveryUsed, err := meter.Int64Counter(Counter2FARecoveryUsedTotal,
		metric.WithDescription("auth-svc recovery code uses (always success — failures route through challenge counter)"),
	)
	if err != nil {
		return nil, err
	}
	twofaDisabled, err := meter.Int64Counter(Counter2FADisabledTotal,
		metric.WithDescription("auth-svc 2FA disable operations (factor=totp|password)"),
	)
	if err != nil {
		return nil, err
	}
	twofaLocked, err := meter.Int64Counter(Counter2FALockedTotal,
		metric.WithDescription("auth-svc 2FA soft-locks (factor=totp|recovery|disable)"),
	)
	if err != nil {
		return nil, err
	}

	return &Counters{
		signup:             signup,
		signin:             signin,
		verifyEmail:        verifyEmail,
		accountLocked:      accountLocked,
		rateLimitTriggered: rateLimitTriggered,
		twofaEnroll:        twofaEnroll,
		twofaChallenge:     twofaChallenge,
		twofaRecoveryUsed:  twofaRecoveryUsed,
		twofaDisabled:      twofaDisabled,
		twofaLocked:        twofaLocked,
	}, nil
}

// SignupResult enumerates the bounded result values for the signup
// counter. Keeping them as typed constants prevents accidental drift
// (e.g., "success" vs "succeeded").
type SignupResult string

const (
	SignupResultSuccess   SignupResult = "success"
	SignupResultFailure   SignupResult = "failure"
	SignupResultDuplicate SignupResult = "duplicate"
)

// IncSignup increments the signup counter with the result label.
func (c *Counters) IncSignup(ctx context.Context, result SignupResult) {
	if c == nil || c.signup == nil {
		return
	}
	c.signup.Add(ctx, 1, metric.WithAttributes(attribute.String(LabelResult, string(result))))
}

// SigninResult enumerates signin outcomes.
type SigninResult string

const (
	SigninResultSuccess SigninResult = "success"
	SigninResultFailure SigninResult = "failure"
)

// IncSignin increments the signin counter with result + error_code
// labels. errorCode SHOULD be empty for success and the canonical
// NNN_xxx form for failure.
func (c *Counters) IncSignin(ctx context.Context, result SigninResult, errorCode string) {
	if c == nil || c.signin == nil {
		return
	}
	c.signin.Add(ctx, 1, metric.WithAttributes(
		attribute.String(LabelResult, string(result)),
		attribute.String(LabelErrorCode, errorCode),
	))
}

// VerifyEmailResult enumerates verify-email outcomes.
type VerifyEmailResult string

const (
	VerifyEmailResultSuccess         VerifyEmailResult = "success"
	VerifyEmailResultExpired         VerifyEmailResult = "expired"
	VerifyEmailResultBruteForce      VerifyEmailResult = "brute_force"
	VerifyEmailResultAlreadyVerified VerifyEmailResult = "already_verified"
	VerifyEmailResultInvalidFormat   VerifyEmailResult = "invalid_format"
)

// IncVerifyEmail increments the verify-email counter.
func (c *Counters) IncVerifyEmail(ctx context.Context, result VerifyEmailResult) {
	if c == nil || c.verifyEmail == nil {
		return
	}
	c.verifyEmail.Add(ctx, 1, metric.WithAttributes(attribute.String(LabelResult, string(result))))
}

// IncAccountLocked increments the account-locked counter. No labels —
// the event itself is the signal; cardinality of additional labels
// would dilute the alerting threshold.
func (c *Counters) IncAccountLocked(ctx context.Context) {
	if c == nil || c.accountLocked == nil {
		return
	}
	c.accountLocked.Add(ctx, 1)
}

// Story 2.4 — 2FA counter helpers. All nil-safe so handler tests that
// don't care about metrics can pass nil Counters.

// TwoFAResult enumerates the outcomes for the 2FA enroll / challenge counters.
type TwoFAResult string

const (
	TwoFAResultSuccess TwoFAResult = "success"
	TwoFAResultFailure TwoFAResult = "failure"
)

// TwoFAFactor enumerates the factor used in a challenge or disable op.
type TwoFAFactor string

const (
	TwoFAFactorTOTP     TwoFAFactor = "totp"
	TwoFAFactorRecovery TwoFAFactor = "recovery"
	TwoFAFactorPassword TwoFAFactor = "password"
	TwoFAFactorDisable  TwoFAFactor = "disable"
)

// Inc2FAEnroll increments the 2FA enrollment counter (init + verify combined;
// the call site distinguishes via the `result` label).
func (c *Counters) Inc2FAEnroll(ctx context.Context, result TwoFAResult) {
	if c == nil || c.twofaEnroll == nil {
		return
	}
	c.twofaEnroll.Add(ctx, 1, metric.WithAttributes(attribute.String(LabelResult, string(result))))
}

// Inc2FAChallenge increments the challenge counter (also fires on
// UseRecoveryCode — factor label distinguishes).
func (c *Counters) Inc2FAChallenge(ctx context.Context, result TwoFAResult, factor TwoFAFactor) {
	if c == nil || c.twofaChallenge == nil {
		return
	}
	c.twofaChallenge.Add(ctx, 1, metric.WithAttributes(
		attribute.String(LabelResult, string(result)),
		attribute.String("factor", string(factor)),
	))
}

// Inc2FARecoveryUsed increments the recovery-code success counter (always
// success — failures route through Inc2FAChallenge with factor=recovery).
func (c *Counters) Inc2FARecoveryUsed(ctx context.Context) {
	if c == nil || c.twofaRecoveryUsed == nil {
		return
	}
	c.twofaRecoveryUsed.Add(ctx, 1)
}

// Inc2FADisabled increments the disable counter (factor label captures
// whether TOTP or password authorized the disable).
func (c *Counters) Inc2FADisabled(ctx context.Context, factor TwoFAFactor) {
	if c == nil || c.twofaDisabled == nil {
		return
	}
	c.twofaDisabled.Add(ctx, 1, metric.WithAttributes(attribute.String("factor", string(factor))))
}

// Inc2FALocked increments the 2FA soft-lock counter (challenge / recovery /
// disable rate-limit exhaustion).
func (c *Counters) Inc2FALocked(ctx context.Context, factor TwoFAFactor) {
	if c == nil || c.twofaLocked == nil {
		return
	}
	c.twofaLocked.Add(ctx, 1, metric.WithAttributes(attribute.String("factor", string(factor))))
}

// RateLimitEndpoint enumerates the rate-limit endpoint surfaces.
type RateLimitEndpoint string

const (
	RateLimitEndpointSignup RateLimitEndpoint = "signup"
	RateLimitEndpointSignin RateLimitEndpoint = "signin"
	RateLimitEndpointResend RateLimitEndpoint = "resend"
)

// RateLimitKeyType enumerates the rate-limit key categories per BR-4.1.
type RateLimitKeyType string

const (
	RateLimitKeyTypeIP    RateLimitKeyType = "ip"
	RateLimitKeyTypeEmail RateLimitKeyType = "email"
)

// IncRateLimitTriggered increments the rate-limit counter with the
// endpoint + key_type labels.
func (c *Counters) IncRateLimitTriggered(ctx context.Context, endpoint RateLimitEndpoint, keyType RateLimitKeyType) {
	if c == nil || c.rateLimitTriggered == nil {
		return
	}
	c.rateLimitTriggered.Add(ctx, 1, metric.WithAttributes(
		attribute.String(LabelEndpoint, string(endpoint)),
		attribute.String(LabelKeyType, string(keyType)),
	))
}
