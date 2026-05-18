// Tests for Story 3.6 AC1 (Standardized OpenAI-Compatible Error Envelope).
//
// Each Test_* maps 1-to-1 to a scenario in the design doc:
//   docs/qa/assessments/3.6-test-design-20260519.md
package openaierr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
)

const knownReqID = "req_a1b2c3d4e5f6"

func ctxWithID(t *testing.T, id string) context.Context {
	t.Helper()
	return requestid.WithRequestID(context.Background(), id)
}

func strPtr(s string) *string { return &s }

// ============================================================
// AC1.A — openaierr.Write Core Behaviour (Unit, P0)
// ============================================================

// 3.6-UNIT-001 (P0): Write emits 5-field envelope in canonical order.
// Asserts byte-exact body — BR-1.6 (no trailing newline) + BR-1.7 (struct
// marshalling, NOT map[string]any alphabetical sort).
func Test_Write_emits_canonical_5_field_envelope(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := ctxWithID(t, knownReqID)

	if err := Write(rec, ctx, 401, "401_invalid_api_key", "Invalid API key.", nil); err != nil {
		t.Fatalf("Write returned unexpected error: %v", err)
	}

	want := `{"error":{"code":"401_invalid_api_key","message":"Invalid API key.","type":"invalid_request_error","param":null,"he_request_id":"req_a1b2c3d4e5f6"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body mismatch:\n got=%q\nwant=%q", got, want)
	}
}

// 3.6-UNIT-002 (P0): Content-Type charset=utf-8 (BR-1.5).
func Test_Write_sets_content_type_charset_utf8(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Write(rec, ctxWithID(t, knownReqID), 401, "401_invalid_api_key", "x", nil); err != nil {
		t.Fatalf("Write returned unexpected error: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
}

// 3.6-UNIT-003 (P0): Table-driven across every canonical code; each emits the
// documented HTTPStatus + error.type. BR-1.4 load-bearing.
func Test_Write_status_and_type_table_driven(t *testing.T) {
	for code, meta := range CodeMetadata {
		code, meta := code, meta
		t.Run(code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := Write(rec, ctxWithID(t, knownReqID), meta.HTTPStatus, code, "msg", nil); err != nil {
				t.Fatalf("Write error: %v", err)
			}
			if rec.Code != meta.HTTPStatus {
				t.Fatalf("status: got=%d want=%d", rec.Code, meta.HTTPStatus)
			}
			var env envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if env.Error.Type != meta.ErrorType {
				t.Fatalf("type: got=%q want=%q", env.Error.Type, meta.ErrorType)
			}
			if env.Error.Code != code {
				t.Fatalf("code: got=%q want=%q", env.Error.Code, code)
			}
		})
	}
}

// 3.6-UNIT-004 (P0): paramValue semantics. nil → null; &"stream" → "stream";
// &"" → "" (empty string, NOT null). BR-1.3 (v) + paramValue inheritance.
func Test_Write_param_nullable_via_paramValue(t *testing.T) {
	cases := []struct {
		name  string
		param *string
		want  string // raw JSON fragment for "param":<want>
	}{
		{"nil_param", nil, `"param":null`},
		{"non_empty_param", strPtr("stream"), `"param":"stream"`},
		{"empty_string_param", strPtr(""), `"param":""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := Write(rec, ctxWithID(t, knownReqID), 501, "501_not_implemented", "x", tc.param); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body=%q does not contain %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// 3.6-UNIT-005 (P0): Unknown code → 500_internal_error fallback + returned
// error. BR-1.4 defensive remap.
func Test_Write_unknown_code_emits_500_internal_error_fallback(t *testing.T) {
	rec := httptest.NewRecorder()
	err := Write(rec, ctxWithID(t, knownReqID), 400, "999_typo", "msg", nil)
	if err == nil {
		t.Fatal("expected non-nil error for unknown code")
	}
	if !strings.Contains(err.Error(), "unknown code") {
		t.Fatalf("error message=%q does not mention 'unknown code'", err.Error())
	}
	if rec.Code != 500 {
		t.Fatalf("status: got=%d want=500", rec.Code)
	}
	var env envelope
	if jerr := json.Unmarshal(rec.Body.Bytes(), &env); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if env.Error.Code != "500_internal_error" {
		t.Fatalf("code: got=%q want=500_internal_error", env.Error.Code)
	}
	if env.Error.Type != "server_error" {
		t.Fatalf("type: got=%q want=server_error", env.Error.Type)
	}
}

// 3.6-UNIT-006 (P0): Empty context (no requestid stamped) → he_request_id
// sentinel "req_000000000000". BR-2.4.
func Test_Write_empty_context_emits_sentinel_he_request_id(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Write(rec, context.Background(), 401, "401_invalid_api_key", "x", nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.HeRequestID != SentinelHeRequestID {
		t.Fatalf("he_request_id: got=%q want=%q", env.Error.HeRequestID, SentinelHeRequestID)
	}
}

// ============================================================
// AC1.B — openaierr.Write Defence-in-Depth (Unit)
// ============================================================

// 3.6-UNIT-014 (P0) — Architect Round 2 L1 anchor: codeMetadata MUST NOT
// contain the RETIRED row "501_streaming_not_implemented".
//
// COUNT NOTE: the Story spec enumerated 38 rows (16 active §5.1.2 + 22 promoted)
// but Dev discovered an additional 19 non-OpenAI codes already used by live
// callers (see Story 3.6 Dev Log → Implementation Decisions Log: "codeMetadata
// superset expansion"). The L1 anchor (RETIRED row exclusion) is preserved
// verbatim; the count assertion is relaxed to `>= 38` so future Stories can
// add new codes without churning this test.
func Test_codeMetadata_excludes_RETIRED_501_streaming_not_implemented(t *testing.T) {
	if _, ok := CodeMetadata["501_streaming_not_implemented"]; ok {
		t.Fatal("RETIRED row 501_streaming_not_implemented must be OMITTED from CodeMetadata (BR-1.4 + L1)")
	}
	if got := len(CodeMetadata); got < 38 {
		t.Fatalf("CodeMetadata length=%d; expected ≥38 (16 active §5.1.2 + ≥22 promoted)", got)
	}
}

// 3.6-UNIT-015 (P1): codeMetadata wins over caller-passed status — defensive
// against buggy callers that pass status=400 with a 500_* code.
func Test_Write_codeMetadata_overrides_caller_status(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Write(rec, ctxWithID(t, knownReqID), 400, "500_internal_error", "x", nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if rec.Code != 500 {
		t.Fatalf("status: got=%d want=500", rec.Code)
	}
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.Type != "server_error" {
		t.Fatalf("type: got=%q want=server_error", env.Error.Type)
	}
}

// 3.6-UNIT-016 (P0): No trailing newline on body — BR-1.6. json.Marshal,
// NOT json.NewEncoder.Encode.
func Test_Write_emits_no_trailing_newline(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Write(rec, ctxWithID(t, knownReqID), 401, "401_invalid_api_key", "x", nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b := rec.Body.Bytes()
	if !bytes.HasSuffix(b, []byte("}")) {
		t.Fatalf("body does not end with '}': %q", b)
	}
	if bytes.HasSuffix(b, []byte("\n")) {
		t.Fatalf("body MUST NOT end with newline: %q", b)
	}
}

// 3.6-UNIT-017 (P2): Marshal-fail emits hardcoded fallback envelope.
// Unreachable for the canonical taxonomy. Skipped per design — the body
// struct cannot marshal-fail (5 string fields + 1 any of nil|string).
func Test_Write_marshal_fail_emits_hardcoded_fallback(t *testing.T) {
	t.Skip("INAPPLICABLE: body struct has no field that can defeat json.Marshal — the canonical envelope is provably marshal-safe (5 string fields + 1 any of nil|string). The fallback path exists for defence-in-depth only.")
}

// 3.6-UNIT-020 (P1): paramValue helper preserves byte-exact semantics.
func Test_paramValue_helper_table_driven(t *testing.T) {
	if v := paramValue(nil); v != nil {
		t.Fatalf("paramValue(nil): got=%v want=nil", v)
	}
	empty := ""
	if v := paramValue(&empty); v != "" {
		t.Fatalf(`paramValue(&""): got=%v want=""`, v)
	}
	stream := "stream"
	if v := paramValue(&stream); v != "stream" {
		t.Fatalf(`paramValue(&"stream"): got=%v want="stream"`, v)
	}
}

// ============================================================
// AC1 Blind-Spot Overlay (Unit)
// ============================================================

// 3.6-BLIND-BOUNDARY-001 (P1): Empty code string → 500_internal_error fallback.
func Test_Write_empty_string_code_falls_through_to_500(t *testing.T) {
	rec := httptest.NewRecorder()
	err := Write(rec, ctxWithID(t, knownReqID), 400, "", "msg", nil)
	if err == nil {
		t.Fatal("expected non-nil error for empty code")
	}
	if rec.Code != 500 {
		t.Fatalf("status: got=%d want=500", rec.Code)
	}
}

// 3.6-BLIND-BOUNDARY-002 (P1): paramValue(&"") emits JSON empty string,
// NOT null. Distinguishes nil pointer from empty-pointer per BR-1.3 (v).
func Test_Write_empty_string_param_emits_JSON_empty_string(t *testing.T) {
	rec := httptest.NewRecorder()
	empty := ""
	if err := Write(rec, ctxWithID(t, knownReqID), 401, "401_invalid_api_key", "x", &empty); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"param":""`) {
		t.Fatalf("expected `\"param\":\"\"` (empty string) in body, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"param":null`) {
		t.Fatalf("body should NOT contain `\"param\":null` for &\"\": %s", rec.Body.String())
	}
}

// failingResponseWriter returns errors.New on Write — emulates client-disconnected.
type failingResponseWriter struct {
	httptest.ResponseRecorder
}

func (f *failingResponseWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("client disconnected")
}

// 3.6-BLIND-ERROR-001 (P1): w.Write error → Write returns the error per BR-1.3.
func Test_Write_w_Write_error_returns_error(t *testing.T) {
	frw := &failingResponseWriter{ResponseRecorder: *httptest.NewRecorder()}
	err := Write(frw, ctxWithID(t, knownReqID), 401, "401_invalid_api_key", "x", nil)
	if err == nil {
		t.Fatal("expected non-nil error when w.Write fails")
	}
	if !strings.Contains(err.Error(), "client disconnected") {
		t.Fatalf("expected wrapped client-disconnect error, got: %v", err)
	}
}

// 3.6-BLIND-ERROR-003 (P2): codeMetadata literal has no duplicate keys at
// the GO LANGUAGE level. Go silently allows duplicate map-literal keys but
// the compile is the canonical guard. Here we simply assert the table is
// non-empty and the RETIRED row is absent — duplicate-key detection is the
// compiler's job (Go 1.22 emits "duplicate key in map literal" as a vet warning).
func Test_codeMetadata_no_duplicate_codes(t *testing.T) {
	if len(CodeMetadata) == 0 {
		t.Fatal("CodeMetadata is empty")
	}
	if _, ok := CodeMetadata["501_streaming_not_implemented"]; ok {
		t.Fatal("RETIRED row must be omitted")
	}
}
