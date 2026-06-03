// Story 5.4 — 5.4-UNIT-036..045 (monthly-cost-reset config validation +
// helpers). The PG/Redis SCAN+DEL paths are exercised by the deferred
// testcontainers integration suite (5.4-INT-016..032).
package main

import (
	"reflect"
	"testing"
)

func envFromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig_MissingPG(t *testing.T) {
	_, err := loadConfig(envFromMap(map[string]string{"HE_API_DB_REDIS_URI": "redis://x"}), false)
	if err == nil || err.Error() != "pg_dsn_missing" {
		t.Fatalf("err=%v want pg_dsn_missing", err)
	}
}

func TestLoadConfig_MissingRedis(t *testing.T) {
	_, err := loadConfig(envFromMap(map[string]string{"HE_API_DB_POSTGRES_URI": "postgres://x"}), false)
	if err == nil || err.Error() != "redis_url_missing" {
		t.Fatalf("err=%v want redis_url_missing", err)
	}
}

func TestLoadConfig_Happy(t *testing.T) {
	c, err := loadConfig(envFromMap(map[string]string{
		"HE_API_DB_POSTGRES_URI":     "postgres://x",
		"HE_API_DB_REDIS_URI":        "redis://y",
		"HE_API_AUDIT_KAFKA_BROKERS": "b1:9092, b2:9092",
		"HE_API_REDIS_CLUSTER_ADDRS": "n1:6379,n2:6379,n3:6379",
		"DRY_RUN":                    "1",
	}), false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !c.dryRun {
		t.Fatal("DRY_RUN=1 must set dryRun")
	}
	if !reflect.DeepEqual(c.kafkaBrokers, []string{"b1:9092", "b2:9092"}) {
		t.Fatalf("kafkaBrokers=%v", c.kafkaBrokers)
	}
	if !reflect.DeepEqual(c.clusterAddrs, []string{"n1:6379", "n2:6379", "n3:6379"}) {
		t.Fatalf("clusterAddrs=%v", c.clusterAddrs)
	}
}

// --purge-redis-only: PG URI not required, Redis URI still required.
func TestLoadConfig_PurgeOnly_IgnoresMissingPG(t *testing.T) {
	c, err := loadConfig(envFromMap(map[string]string{"HE_API_DB_REDIS_URI": "redis://y"}), true)
	if err != nil {
		t.Fatalf("purge-only must not require PG; err=%v", err)
	}
	if !c.purgeOnly {
		t.Fatal("purgeOnly flag must propagate")
	}
}

func TestLoadConfig_PurgeOnly_StillNeedsRedis(t *testing.T) {
	_, err := loadConfig(envFromMap(map[string]string{}), true)
	if err == nil || err.Error() != "redis_url_missing" {
		t.Fatalf("err=%v want redis_url_missing", err)
	}
}

func TestSplitNonEmpty(t *testing.T) {
	got := splitNonEmpty(" a , ,b,  , c ")
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

func TestAuditRetryBackoff_ThreeAttempts(t *testing.T) {
	if len(auditRetryBackoff) != 3 {
		t.Fatalf("want 3 attempts (BR-3.9 m-3); got %d", len(auditRetryBackoff))
	}
}
