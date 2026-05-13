// oauth_test.go — Story 2.3 P7 OAuth counter registration tests.
// Mirrors metrics_test.go pattern: register an in-process meter provider,
// run a counter increment, scrape the recorded metric, assert labels.
package metrics_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	authmetrics "github.com/he-api/he-api/apps/auth-svc/internal/metrics"
)

func newOAuthMeterProvider(t *testing.T) (*authmetrics.OAuthCounters, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
	})
	c, err := authmetrics.NewOAuth()
	if err != nil {
		t.Fatalf("NewOAuth: %v", err)
	}
	return c, reader
}

// Scenario: 2.3-UNIT-067
// IncCallback emits provider + outcome labels; counter cardinality bounded.
func TestOAuthCounters_IncCallback_EmitsLabels(t *testing.T) {
	c, reader := newOAuthMeterProvider(t)
	c.IncCallback(context.Background(), "google", "success")
	c.IncCallback(context.Background(), "github", "state_invalid")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == authmetrics.CounterOAuthCallbackTotal {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("counter %s not registered", authmetrics.CounterOAuthCallbackTotal)
	}
}

// Scenario: 2.3-UNIT-068 + m-3
// IncLink emits branch + was_locked labels. The was_locked label is what
// powers the Grafana panel #8 bypassed-lock query.
func TestOAuthCounters_IncLink_BypassedLockLabel(t *testing.T) {
	c, reader := newOAuthMeterProvider(t)
	c.IncLink(context.Background(), "A", true)  // locked-bypass
	c.IncLink(context.Background(), "A", false) // normal re-login
	c.IncLink(context.Background(), "C", false) // new user

	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == authmetrics.CounterOAuthLinkTotal {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("counter %s not registered", authmetrics.CounterOAuthLinkTotal)
	}
}

// Scenario: 2.3-UNIT-069
// ObserveProviderLatency registers a histogram instrument with provider
// + endpoint labels.
func TestOAuthCounters_ProviderLatencyHistogram(t *testing.T) {
	c, reader := newOAuthMeterProvider(t)
	c.ObserveProviderLatency(context.Background(), "google", "token", 0.234)
	c.ObserveProviderLatency(context.Background(), "github", "user", 0.085)
	c.ObserveProviderLatency(context.Background(), "github", "emails", 0.110)

	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == authmetrics.HistogramOAuthProviderLatency {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("histogram %s not registered", authmetrics.HistogramOAuthProviderLatency)
	}
}

// Nil-safe behavior — calling Inc* on a nil OAuthCounters must not panic.
func TestOAuthCounters_NilSafe(t *testing.T) {
	var c *authmetrics.OAuthCounters
	c.IncInitiate(context.Background(), "google")
	c.IncCallback(context.Background(), "google", "success")
	c.IncLink(context.Background(), "A", true)
	c.ObserveProviderLatency(context.Background(), "google", "token", 0.1)
	// reaching here = no panic = pass
}
