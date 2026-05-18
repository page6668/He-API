package openaierr

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
)

// body is the §5.1.2 error envelope payload. Field order is locked to the
// canonical sequence (BR-1.7 + Architect Round 1 OQ2 RATIFIED):
//
//	code → message → type → param → he_request_id
//
// Struct-based marshalling is required: map[string]any would sort keys
// alphabetically (Go stdlib) and break the byte-exact golden in UNIT-001.
type body struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Type        string `json:"type"`
	Param       any    `json:"param"`
	HeRequestID string `json:"he_request_id"`
}

type envelope struct {
	Error body `json:"error"`
}

// SentinelHeRequestID is the value emitted when the request context carries
// no request-id (probe routes, unit tests without middleware). Documented in
// AC1 Data Validation row + BR-2.4 fallback.
const SentinelHeRequestID = "req_000000000000"

// fallbackBody is emitted when json.Marshal fails — unreachable for the
// canonical taxonomy (5 string fields + 1 nullable string), preserved for
// defence-in-depth per AC1 Error Handling row 2.
var fallbackBody = []byte(`{"error":{"code":"500_internal_error","message":"Internal error.","type":"server_error","param":null,"he_request_id":"req_000000000000"}}`)

// Write emits the canonical §5.1.2 5-field error envelope.
//
// Per BR-1.4, the HTTP status + error.type are derived from CodeMetadata —
// caller-passed status is IGNORED if codeMetadata has a different value (UNIT-015).
// Unknown codes trigger the defensive 500_internal_error fallback (UNIT-005).
//
// Per BR-1.3 + Architect Round 1 OQ3 RATIFIED, Write returns the error from
// w.Write so callers can attach telemetry hooks (Story 9.x). The unknown-code
// path also returns a non-nil error so the caller can log on its own.
//
// Per BR-1.5, Content-Type is set to "application/json; charset=utf-8".
// Per BR-1.6, the body is emitted via json.Marshal — NO trailing newline.
// Per BR-2.4, when the context carries no request-id, the sentinel
// "req_000000000000" is emitted.
func Write(w http.ResponseWriter, ctx context.Context, status int, code, message string, param *string) error {
	meta, ok := CodeMetadata[code]
	var fallbackErr error
	if !ok {
		slog.ErrorContext(ctx, "openaierr_unknown_code", slog.String("requested_code", code))
		fallbackErr = errors.New("openaierr: unknown code " + code)
		code = "500_internal_error"
		meta = CodeMetadata[code]
		_ = status // caller-supplied status discarded; codeMetadata wins
	}

	reqID, hasID := requestid.FromContext(ctx)
	if !hasID || reqID == "" {
		reqID = SentinelHeRequestID
	}

	payload := envelope{Error: body{
		Code:        code,
		Message:     message,
		Type:        meta.ErrorType,
		Param:       paramValue(param),
		HeRequestID: reqID,
	}}

	buf, err := json.Marshal(payload)
	if err != nil {
		// Unreachable for the canonical taxonomy; defence-in-depth per
		// AC1 Error Handling row 2.
		slog.ErrorContext(ctx, "openaierr_marshal_failed",
			slog.String("code", code),
			slog.String("error", err.Error()),
		)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, writeErr := w.Write(fallbackBody)
		if writeErr != nil {
			return writeErr
		}
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(meta.HTTPStatus)
	if _, err := w.Write(buf); err != nil {
		return err
	}
	return fallbackErr
}

// paramValue returns the JSON-rendered value for the envelope's `param` field.
// nil → JSON null; non-nil → JSON string (including empty string per BR-1.3 (v)).
// Migrated verbatim from chat_completions.go:377 (Story 3.3 semantics preserved).
func paramValue(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
