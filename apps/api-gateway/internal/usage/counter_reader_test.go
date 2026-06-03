// Story 5.2 — UNIT-090..093 (monthly-cost counter reader).
package usage_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/usage"
)

func TestReadMonthlyCostUSD(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	ctx := context.Background()
	const id = "00000000-0000-4000-8000-000000000001"

	t.Run("present → value,true,nil", func(t *testing.T) {
		_ = mini.Set(usage.CounterKey(id), "25.50")
		v, found, err := usage.ReadMonthlyCostUSD(ctx, rdb, id)
		if err != nil || !found || v != "25.50" {
			t.Fatalf("got (%q,%v,%v) want (25.50,true,nil)", v, found, err)
		}
	})

	t.Run("missing → empty,false,nil (BR-4.4)", func(t *testing.T) {
		v, found, err := usage.ReadMonthlyCostUSD(ctx, rdb, "00000000-0000-4000-8000-000000000099")
		if err != nil || found || v != "" {
			t.Fatalf("got (%q,%v,%v) want ('',false,nil)", v, found, err)
		}
	})

	t.Run("nil client → empty,false,nil", func(t *testing.T) {
		v, found, err := usage.ReadMonthlyCostUSD(ctx, nil, id)
		if err != nil || found || v != "" {
			t.Fatalf("got (%q,%v,%v) want ('',false,nil)", v, found, err)
		}
	})

	t.Run("Redis error → empty,false,err (fail-open signal)", func(t *testing.T) {
		mini.Close()
		_, _, err := usage.ReadMonthlyCostUSD(ctx, rdb, id)
		if err == nil {
			t.Fatal("want transport error after Redis close")
		}
	})

	t.Run("CounterKey shape", func(t *testing.T) {
		if got := usage.CounterKey("abc"); got != "usage:apikey:abc:month_cost_usd" {
			t.Fatalf("CounterKey=%q", got)
		}
	})
}
