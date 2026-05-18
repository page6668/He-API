// Story 3.1 — BenchmarkColdStart (benchstat-friendly informational variant).
//
// The CI gate uses TestColdStart (P95 assertion) in coldstart_test.go; this
// benchmark exists for local tuning:
//
//	go test -bench=BenchmarkColdStart -benchmem -count=10 \
//	  ./apps/api-gateway/cmd/server
//
// Output is benchstat-compatible via b.ReportMetric. The fixture (binary
// path, stub upstreams, RSA key) is the same one TestMain sets up for
// coldstart_test.go.
package main

import (
	"testing"
	"time"
)

// Scenario: 3.1-INT-028
// Priority: P1 | Level: integration | BR: BR-2.8
func BenchmarkColdStart(b *testing.B) {
	if gatewayBinPath == "" {
		b.Skip("gatewayBinPath not set; TestMain did not run")
	}
	for i := 0; i < b.N; i++ {
		dur, _, g, err := runOneColdStart(gatewayBinPath, nil, nil, 5*time.Second)
		if g != nil {
			g.stop(2 * time.Second)
		}
		if err != nil {
			b.Fatalf("iter %d: %v", i+1, err)
		}
		b.ReportMetric(float64(dur.Milliseconds()), "ms/op")
	}
}
