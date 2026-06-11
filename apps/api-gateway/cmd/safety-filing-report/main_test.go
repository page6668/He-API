package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var fixedNow = func() time.Time { return time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC) }

func validCfg() config {
	return config{
		start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		end:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		out:   "filing.pdf",
	}
}

// 8.5-UNIT-024 — invalid range (start ≥ end) → exit 2, NO DB query (validated
// before any pool work).
func Test8_5_UNIT024_InvalidRangeExitsTwo(t *testing.T) {
	cases := [][]string{
		{"--start", "2026-07-01", "--end", "2026-01-01", "--out", "x.pdf"}, // start > end
		{"--start", "2026-01-01", "--end", "2026-01-01", "--out", "x.pdf"}, // start == end (half-open)
		{"--start", "not-a-date", "--end", "2026-07-01", "--out", "x.pdf"}, // bad date
		{"--start", "2026-01-01", "--end", "2026-07-01"},                   // missing --out
	}
	for _, args := range cases {
		if _, code, ok := parseArgs(args, testLogger()); ok || code != 2 {
			t.Fatalf("args %v: code=%d ok=%v, want code=2 ok=false", args, code, ok)
		}
	}
}

// parseArgs accepts a valid half-open range.
func Test8_5_ParseArgsValid(t *testing.T) {
	cfg, code, ok := parseArgs([]string{"--start", "2026-01-01", "--end", "2026-07-01", "--out", "f.pdf"}, testLogger())
	if !ok || code != 0 {
		t.Fatalf("valid args rejected: code=%d ok=%v", code, ok)
	}
	if cfg.out != "f.pdf" || !cfg.start.Before(cfg.end) {
		t.Fatalf("bad cfg: %+v", cfg)
	}
}

// 8.5-UNIT-020 (cmd) — generate over rows → a well-formed PDF written, exit 0.
func Test8_5_GenerateWritesPDF(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	cfg := validCfg()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(cfg.start, cfg.end).
		WillReturnRows(pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"}).
			AddRow("input", "term_alpha", "strict", int64(4)))

	var gotPath string
	var gotBytes []byte
	wf := func(p string, b []byte, _ os.FileMode) error { gotPath, gotBytes = p, b; return nil }

	if code := generate(context.Background(), testLogger(), mock, cfg, fixedNow, wf); code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	if gotPath != "filing.pdf" {
		t.Fatalf("wrote to %q want filing.pdf", gotPath)
	}
	if !bytes.HasPrefix(gotBytes, []byte("%PDF-")) {
		t.Fatal("written bytes are not a PDF")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 8.5-UNIT-022 (cmd) — empty range → a valid PDF still written, exit 0.
func Test8_5_GenerateEmptyRangeExitsZero(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	cfg := validCfg()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(cfg.start, cfg.end).
		WillReturnRows(pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"}))
	written := false
	wf := func(string, []byte, os.FileMode) error { written = true; return nil }
	if code := generate(context.Background(), testLogger(), mock, cfg, fixedNow, wf); code != 0 {
		t.Fatalf("exit=%d want 0 on empty range", code)
	}
	if !written {
		t.Fatal("empty-range report should still write a PDF")
	}
}

// 8.5-BLIND-ERROR-004 — DB unreachable → exit 1 and NO partial PDF written.
func Test8_5_BLIND_ERROR004_DBErrorNoPartialPDF(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	cfg := validCfg()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(cfg.start, cfg.end).
		WillReturnError(errors.New("connection refused"))
	wrote := false
	wf := func(string, []byte, os.FileMode) error { wrote = true; return nil }
	if code := generate(context.Background(), testLogger(), mock, cfg, fixedNow, wf); code != 1 {
		t.Fatalf("exit=%d want 1 on DB error", code)
	}
	if wrote {
		t.Fatal("no partial PDF must be written on DB failure")
	}
}

// generate maps a write failure to exit 1.
func Test8_5_GenerateWriteErrorExitsOne(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	cfg := validCfg()
	mock.ExpectQuery("FROM he_api.content_safety_logs").
		WithArgs(cfg.start, cfg.end).
		WillReturnRows(pgxmock.NewRows([]string{"direction", "matched_rule", "strictness", "count"}))
	wf := func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	if code := generate(context.Background(), testLogger(), mock, cfg, fixedNow, wf); code != 1 {
		t.Fatalf("exit=%d want 1 on write error", code)
	}
}
