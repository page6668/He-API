module github.com/he-api/he-api/packages/safety-lexicon

// go 1.25.0 (NOT 1.22): the OQ-8.1-4 ruling adopts golang.org/x/text v0.23.0 for
// correct NFKC, and that module requires go >= 1.23 — so the Architect Round-1
// Low #1 "keep go 1.22" note (premised on zero third-party deps) no longer
// applies. Matches the two sibling modules that already resolve x/text v0.23.0
// (apps/billing-svc, apps/payment-svc) and the go.work toolchain (1.25.0).
go 1.25.0

require golang.org/x/text v0.23.0
