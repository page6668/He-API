// Story 8.4 — end-to-end INPUT-path severity gating + cross-direction
// resolve-once over the full ServeHTTP path (8.4-UNIT-017/021 + INT-010/011).
// Black-box (handlers_test): a seeded multi-severity scanner + a per-request
// strictness injected via the bearer CachedClaims context.
package handlers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// seededScanner builds a floor-satisfying standalone lexicon (benign padding +
// the supplied seed terms) so a black-box test controls per-severity terms.
func seededScanner(extra ...safetylexicon.Row) *contentsafety.Scanner {
	rows := make([]safetylexicon.Row, 0, 1700+len(extra))
	for i := 0; i < 1100; i++ {
		rows = append(rows, safetylexicon.Row{
			Lang: safetylexicon.LangZH, Category: safetylexicon.CategoryOther, Severity: safetylexicon.SeverityLow,
			Raw: fmt.Sprintf("填充占位字%05d", i), File: "t/zh/pad.txt", Line: i + 1,
		})
	}
	for i := 0; i < 600; i++ {
		rows = append(rows, safetylexicon.Row{
			Lang: safetylexicon.LangEN, Category: safetylexicon.CategoryOther, Severity: safetylexicon.SeverityLow,
			Raw: fmt.Sprintf("padinputen%05d", i), File: "t/en/pad.txt", Line: i + 1,
		})
	}
	rows = append(rows, extra...)
	return contentsafety.NewScanner(safetylexicon.NewFromRegistry(safetylexicon.Registry{Rows: rows}))
}

func enSeed(raw string, cat safetylexicon.Category, sev safetylexicon.Severity, line int) safetylexicon.Row {
	return safetylexicon.Row{Lang: safetylexicon.LangEN, Category: cat, Severity: sev, Raw: raw, File: "t/en/seed.txt", Line: line}
}

// doRequestStrictness drives ServeHTTP with the bearer context PLUS a CachedClaims
// carrying the per-Key level (the seam ServeHTTP resolves once).
func doRequestStrictness(t *testing.T, h *handlers.ChatCompletionsHandler, body, level string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer he-test-key-stub")
	ctx := middleware.WithAPIKeyID(req.Context(), testAPIKeyID)
	ctx = middleware.BearerWithUserID(ctx, testUserID)
	ctx = middleware.WithCacheValue(ctx, &middleware.CachedClaims{
		APIKeyID:                testAPIKeyID,
		UserID:                  testUserID,
		ContentSafetyStrictness: level,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func isContentFilter(t *testing.T, rr *httptest.ResponseRecorder) bool {
	if rr.Code != http.StatusBadRequest {
		return false
	}
	errObj, _ := decodeBody(t, rr)["error"].(map[string]any)
	return errObj != nil && errObj["code"] == "400_content_filter"
}

// 8.4-UNIT-017 — input reject is gated by the resolved level: a MEDIUM input term
// blocks under default/strict but DISPATCHES under loose.
func TestSafetyStrictness_InputGated(t *testing.T) {
	scanner := seededScanner(enSeed("medinword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, 8801))
	h := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithSafetyScanner(scanner))
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"please consider medinword carefully"}]}`

	if !isContentFilter(t, doRequestStrictness(t, h, body, "default")) {
		t.Fatal("default: a medium input term must be blocked (medium >= medium)")
	}
	if !isContentFilter(t, doRequestStrictness(t, h, body, "strict")) {
		t.Fatal("strict: a medium input term must be blocked (block-all)")
	}
	if rr := doRequestStrictness(t, h, body, "loose"); rr.Code != http.StatusOK {
		t.Fatalf("loose: a medium input term must DISPATCH (medium < high), got status=%d body=%s", rr.Code, rr.Body.String())
	}
}

// 8.4-UNIT-017b — first-qualifying across messages on the input path: under loose,
// a low term in an earlier message must not mask a high term in a later message.
func TestSafetyStrictness_InputFirstQualifying(t *testing.T) {
	scanner := seededScanner(
		enSeed("lowinword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 8810),
		enSeed("highinword", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 8811),
	)
	h := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithSafetyScanner(scanner))
	// msg[0] has a LOW term (sub-threshold under loose), msg[1] has a HIGH term.
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"mentions lowinword first"},{"role":"user","content":"then highinword later"}]}`
	if !isContentFilter(t, doRequestStrictness(t, h, body, "loose")) {
		t.Fatal("loose: the low term must not mask the later high term (first-qualifying across messages)")
	}
	// Only the low term → loose dispatches.
	lowOnly := `{"model":"qwen-max","messages":[{"role":"user","content":"only lowinword here"}]}`
	if rr := doRequestStrictness(t, h, lowOnly, "loose"); rr.Code != http.StatusOK {
		t.Fatalf("loose: a low-only request must dispatch, got %d", rr.Code)
	}
}

// 8.4-UNIT-021 / G4 — strict input gating is byte-identical to the pre-8.4
// block-all: a low term blocks under strict (== ScanText block-all). Also: an
// ABSENT CacheValue (no claims) fails closed to strict and blocks the low term.
func TestSafetyStrictness_StrictAndFailClosedBlockAll(t *testing.T) {
	scanner := seededScanner(enSeed("lowinword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 8820))
	h := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithSafetyScanner(scanner))
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"contains lowinword token"}]}`

	if !isContentFilter(t, doRequestStrictness(t, h, body, "strict")) {
		t.Fatal("strict: a low term must block (block-all baseline)")
	}
	// Fail-closed: NO CacheValue at all (doRequest sets bearer ctx but no claims) →
	// resolver → strict → the low term blocks. This is the rollout/absent-claims gate.
	if !isContentFilter(t, doRequest(t, h, body)) {
		t.Fatal("absent claims must fail-closed to strict and block the low term")
	}
	// A garbage/unknown token also fails closed to strict.
	if !isContentFilter(t, doRequestStrictness(t, h, body, "totally-bogus")) {
		t.Fatal("unknown strictness token must fail-closed to strict and block")
	}
}

// 8.4-INT-010 — resolve-once / cross-direction: one request under loose resolves a
// single level; a medium INPUT term dispatches (input not blocked) AND the clean
// mock OUTPUT is not redacted — both directions agree under the one level.
func TestSafetyStrictness_CrossDirectionResolveOnce(t *testing.T) {
	in := seededScanner(enSeed("medinword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, 8830))
	out := seededScanner(enSeed("medoutword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, 8831))
	h := handlers.NewChatCompletionsHandler(discardLogger(),
		handlers.WithSafetyScanner(in),
		handlers.WithOutputSafetyScanner(out),
	)
	// loose + a medium input term → input dispatches; the mock output is clean →
	// not redacted; final 200 (both directions gated by the single loose level).
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"medinword in the prompt"}]}`
	rr := doRequestStrictness(t, h, body, "loose")
	if rr.Code != http.StatusOK {
		t.Fatalf("loose cross-direction: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// Under strict the SAME medium input term is blocked at the input — proving the
	// single resolved level drives the input gate.
	if !isContentFilter(t, doRequestStrictness(t, h, body, "strict")) {
		t.Fatal("strict: the medium input term must block (one level, both directions)")
	}
}
