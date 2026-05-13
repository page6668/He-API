// oauth.go — Story 2.3 P7 OAuth Prometheus counters per BR-4.7.
//
// Four instruments registered on the same OTel meter as Story 2.2 counters
// (constants kept greppable so Grafana dashboard + Alertmanager rule
// templates pattern-match cleanly):
//
//   auth_oauth_initiate_total{provider}            — initiate volume / provider
//   auth_oauth_callback_total{provider, outcome}   — callback volume + outcome dist
//   auth_oauth_link_total{branch, was_locked}      — 10-branch matrix counters
//                                                    incl. m-3 bypassed-lock panel
//   auth_oauth_provider_latency_seconds{provider, endpoint}
//                                                  — token + /user histogram
//
// Cardinality is bounded — provider ∈ {google, github}; outcome ∈ {success,
// state_invalid, provider_error, email_not_verified, link_unverified,
// subject_mismatch, cross_provider, suspended, ...}; branch ∈ the 11
// LinkBranch constants; endpoint ∈ {token, user, emails}.
package metrics

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OAuth counter name constants — referenced by Grafana dashboard JSON
// (infra/grafana-dashboards/oauth.json) + Alertmanager rules.
const (
	CounterOAuthInitiateTotal     = "auth_oauth_initiate_total"
	CounterOAuthCallbackTotal     = "auth_oauth_callback_total"
	CounterOAuthLinkTotal         = "auth_oauth_link_total"
	HistogramOAuthProviderLatency = "auth_oauth_provider_latency_seconds"
)

// Label keys for the OAuth counter set.
const (
	LabelProvider   = "provider"
	LabelOutcome    = "outcome"
	LabelBranch     = "branch"
	LabelWasLocked  = "was_locked"
	LabelOAuthEndpt = "endpoint"
)

// OAuthCounters is the bundle of OAuth metric instruments. Held alongside
// `Counters` on AuthServer; cmd/server constructs both at startup.
type OAuthCounters struct {
	initiate        metric.Int64Counter
	callback        metric.Int64Counter
	link            metric.Int64Counter
	providerLatency metric.Float64Histogram
}

// NewOAuth registers the 4 OAuth instruments. Failure is fatal in
// cmd/server (silent metric loss is worse than crash).
func NewOAuth() (*OAuthCounters, error) {
	meter := otel.GetMeterProvider().Meter(MeterName)
	initiate, err := meter.Int64Counter(CounterOAuthInitiateTotal,
		metric.WithDescription("auth-svc OAuth initiate volume by provider"),
	)
	if err != nil {
		return nil, err
	}
	callback, err := meter.Int64Counter(CounterOAuthCallbackTotal,
		metric.WithDescription("auth-svc OAuth callback volume by provider+outcome"),
	)
	if err != nil {
		return nil, err
	}
	link, err := meter.Int64Counter(CounterOAuthLinkTotal,
		metric.WithDescription("auth-svc OAuth link outcomes by branch + was_locked (m-3 bypassed-lock panel)"),
	)
	if err != nil {
		return nil, err
	}
	hist, err := meter.Float64Histogram(HistogramOAuthProviderLatency,
		metric.WithDescription("auth-svc OAuth provider RTT (token|user|emails endpoints) seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	return &OAuthCounters{
		initiate:        initiate,
		callback:        callback,
		link:            link,
		providerLatency: hist,
	}, nil
}

// IncInitiate increments the initiate counter with provider label.
func (c *OAuthCounters) IncInitiate(ctx context.Context, provider string) {
	if c == nil || c.initiate == nil {
		return
	}
	c.initiate.Add(ctx, 1, metric.WithAttributes(attribute.String(LabelProvider, provider)))
}

// IncCallback increments the callback counter with provider + outcome.
func (c *OAuthCounters) IncCallback(ctx context.Context, provider, outcome string) {
	if c == nil || c.callback == nil {
		return
	}
	c.callback.Add(ctx, 1, metric.WithAttributes(
		attribute.String(LabelProvider, provider),
		attribute.String(LabelOutcome, outcome),
	))
}

// IncLink increments the link counter with branch + was_locked.
func (c *OAuthCounters) IncLink(ctx context.Context, branch string, wasLocked bool) {
	if c == nil || c.link == nil {
		return
	}
	c.link.Add(ctx, 1, metric.WithAttributes(
		attribute.String(LabelBranch, branch),
		attribute.String(LabelWasLocked, strconv.FormatBool(wasLocked)),
	))
}

// ObserveProviderLatency records a provider RTT measurement.
func (c *OAuthCounters) ObserveProviderLatency(ctx context.Context, provider, endpoint string, seconds float64) {
	if c == nil || c.providerLatency == nil {
		return
	}
	c.providerLatency.Record(ctx, seconds, metric.WithAttributes(
		attribute.String(LabelProvider, provider),
		attribute.String(LabelOAuthEndpt, endpoint),
	))
}
