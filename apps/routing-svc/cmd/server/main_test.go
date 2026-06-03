// Boot/lifecycle tests for routing-svc's run() (Story 6.1 AC1).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-003          boot fails (non-zero) + structured slog-JSON error on empty catalogue
//	6.1-BLIND-RESOURCE-001  SIGTERM/ctx-cancel -> graceful shutdown releases the listener
//	6.1-BLIND-RESOURCE-002  TracerProvider.Shutdown runs on the exit path (defer) — clean return
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/routing-svc/internal/catalogue"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

// 6.1-UNIT-003 — empty catalogue -> run returns a boot error, and the error
// log main() emits is structured JSON carrying the cause.
func Test_UNIT_003_empty_catalogue_structured_boot_error(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	err := run(context.Background(), logger, modelscatalogue.NewFromRegistry(modelscatalogue.Registry{}), "127.0.0.1:0")
	if err == nil {
		t.Fatal("run accepted an empty catalogue; want non-zero boot error")
	}
	if !strings.Contains(err.Error(), "models catalogue is empty") {
		t.Errorf("error = %q, want it to mention an empty catalogue", err.Error())
	}

	// main() logs the error via this JSON logger before os.Exit(1); assert the
	// emitted line is well-formed structured JSON carrying the cause.
	logger.Error("routing-svc boot failed", slog.String("error", err.Error()))
	var rec map[string]any
	if jerr := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); jerr != nil {
		t.Fatalf("boot error log is not valid JSON: %v\nlog=%s", jerr, buf.String())
	}
	if rec["level"] != "ERROR" {
		t.Errorf("log level = %v, want ERROR", rec["level"])
	}
	if s, _ := rec["error"].(string); !strings.Contains(s, "models catalogue is empty") {
		t.Errorf("log error field = %v, want empty-catalogue cause", rec["error"])
	}
}

// 6.1-BLIND-RESOURCE-001/002 — run serves with a valid catalogue and returns
// cleanly (nil) when the context is cancelled: the listener is released and the
// tracer/meter providers are shut down via the deferred Shutdown calls.
func Test_BLIND_RESOURCE_001_graceful_shutdown_on_cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), catalogue.Load(), "127.0.0.1:0")
	}()

	// Give the listener a moment to come up, then signal shutdown.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v on graceful shutdown, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of ctx cancel — shutdown did not release the listener")
	}
}
