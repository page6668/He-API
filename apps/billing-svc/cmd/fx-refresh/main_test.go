package main

import "testing"

// 7.2-INT-030 (binary side) — the dedicated fx-refresh `main` package builds and
// validates its config. The CronJob-targets-this-binary assertion lives in the
// Helm manifest fixture test; here we lock the config contract.

// loadConfig fails fast on a missing PG DSN (infra error → exit 1, not stale-serve).
func TestLoadConfig_MissingPG(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("missing HE_API_DB_POSTGRES_URI must fail fast")
	}
}

// loadConfig selects the manual override (dev/CI) and labels the row source.
func TestLoadConfig_ManualOverride(t *testing.T) {
	env := map[string]string{
		"HE_API_DB_POSTGRES_URI": "postgres://x/y",
		"FX_MANUAL_USD_CNY":      "7.25",
	}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.source != "manual" || cfg.provider == nil {
		t.Fatalf("manual override not selected: %+v", cfg)
	}
}

// loadConfig fails fast when PG is set but no provider is configured.
func TestLoadConfig_NoProvider(t *testing.T) {
	env := map[string]string{"HE_API_DB_POSTGRES_URI": "postgres://x/y"}
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
		t.Fatal("no provider configured must fail fast")
	}
}

// loadConfig selects the HTTP provider when a base URL is set (default source).
func TestLoadConfig_HTTPProvider(t *testing.T) {
	env := map[string]string{
		"HE_API_DB_POSTGRES_URI": "postgres://x/y",
		"HE_API_FX_PROVIDER_URL": "https://example.test/latest/USD",
	}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.source != "http" || cfg.provider == nil {
		t.Fatalf("http provider not selected: %+v", cfg)
	}
}
