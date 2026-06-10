package templates_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/he-api/he-api/apps/notification-svc/internal/templates"
)

// Scenario: P2e / 2 — Renderer happy path.
// Render returns subject + text body + HTML body with snake_case vars
// substituted; subject is the first non-comment line of the .txt template.
// Story 7.7 — 7.7-UNIT-052: the low_balance template + the auto-recharge-failed
// variant resolve to the correct per-locale (en / zh-CN) html+txt with the
// string-decimal money vars substituted. Mirrors the 5.4 cap_warning precedent.
func TestRenderer_LowBalance_Localized(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	vars := map[string]string{"display_name": "Alice", "current_balance": "4.86", "threshold": "5.00"}

	en, err := r.Render(templates.TemplateLowBalance, "en", vars)
	if err != nil {
		t.Fatalf("Render low_balance en: %v", err)
	}
	if !strings.Contains(en.TextBody, "$4.86") || !strings.Contains(en.TextBody, "$5.00") {
		t.Errorf("low_balance en did not substitute string-decimal money: %q", en.TextBody)
	}
	if !strings.Contains(en.HTMLBody, "<!DOCTYPE html>") {
		t.Errorf("low_balance en HTML missing DOCTYPE")
	}

	zh, err := r.Render(templates.TemplateLowBalance, "zh-CN", vars)
	if err != nil {
		t.Fatalf("Render low_balance zh-CN: %v", err)
	}
	if zh.Subject == en.Subject {
		t.Errorf("expected localized subject to differ between en and zh-CN")
	}

	failed, err := r.Render(templates.TemplateLowBalanceFailed, "en", vars)
	if err != nil {
		t.Fatalf("Render low_balance_failed en: %v", err)
	}
	if !strings.Contains(strings.ToLower(failed.Subject), "auto-recharge failed") {
		t.Errorf("failed-variant subject = %q, want it to mention auto-recharge failed", failed.Subject)
	}
}

func TestRenderer_EmailVerification_HappyPath(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	got, err := r.Render(templates.TemplateEmailVerification, "en", map[string]string{
		"token":             "TOKEN-XYZ",
		"verification_link": "https://console.he-api.com/en/verify-email?token=TOKEN-XYZ",
	})
	if err != nil {
		t.Fatalf("Render(en): %v", err)
	}
	if got.Subject != "Verify your He-API email" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Verify your He-API email")
	}
	if !strings.Contains(got.TextBody, "https://console.he-api.com/en/verify-email?token=TOKEN-XYZ") {
		t.Errorf("TextBody missing verification link substitution; got:\n%s", got.TextBody)
	}
	if !strings.Contains(got.HTMLBody, "https://console.he-api.com/en/verify-email?token=TOKEN-XYZ") {
		t.Errorf("HTMLBody missing verification link substitution")
	}
	// HTML body MUST contain the full doctype + a button-style anchor.
	if !strings.Contains(got.HTMLBody, "<!DOCTYPE html>") {
		t.Errorf("HTMLBody missing DOCTYPE — render dropped HTML head?")
	}
	// Subject MUST NOT contain the TODO marker line for non-en locales.
	gotZH, err := r.Render(templates.TemplateEmailVerification, "zh-CN", map[string]string{
		"token":             "T",
		"verification_link": "https://x.example/v",
	})
	if err != nil {
		t.Fatalf("Render(zh-CN): %v", err)
	}
	if strings.Contains(gotZH.Subject, "TODO") || strings.HasPrefix(gotZH.Subject, "#") {
		t.Errorf("zh-CN subject leaked TODO/comment marker: %q", gotZH.Subject)
	}
	if !strings.Contains(gotZH.TextBody, "https://x.example/v") {
		t.Errorf("zh-CN TextBody did not substitute verification_link")
	}
}

// Scenario: P2e / 2 — locale fallback.
// Unknown locale falls back to FallbackLocale (en).
func TestRenderer_UnknownLocaleFallsBackToEn(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	got, err := r.Render(templates.TemplateEmailVerification, "klingon", map[string]string{
		"token":             "T",
		"verification_link": "https://x.example/v",
	})
	if err != nil {
		t.Fatalf("Render(klingon): %v", err)
	}
	// English subject is the canonical text from en.txt.
	if got.Subject != "Verify your He-API email" {
		t.Fatalf("Subject = %q, want English fallback %q", got.Subject, "Verify your He-API email")
	}
}

// Empty locale also falls back to en (BR-1.9 default).
func TestRenderer_EmptyLocaleFallsBackToEn(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	got, err := r.Render(templates.TemplateEmailVerification, "", map[string]string{
		"token":             "T",
		"verification_link": "https://x.example/v",
	})
	if err != nil {
		t.Fatalf("Render(\"\"): %v", err)
	}
	if got.Subject != "Verify your He-API email" {
		t.Fatalf("Subject = %q, want English fallback", got.Subject)
	}
}

// Scenario: P2e / 2 — unknown template name returns ErrTemplateNotFound.
func TestRenderer_UnknownTemplateNameReturnsErrTemplateNotFound(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	_, err := r.Render("definitely_not_a_template", "en", map[string]string{})
	if !errors.Is(err, templates.ErrTemplateNotFound) {
		t.Fatalf("Render(unknown) = %v, want ErrTemplateNotFound", err)
	}
}

// Scenario: P2e / 2 — missing required variable surfaces ErrMissingVariable
// (Missingkey=error option on the underlying template engine).
func TestRenderer_MissingVariableReturnsErrMissingVariable(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	_, err := r.Render(templates.TemplateEmailVerification, "en", map[string]string{
		"token": "T", // verification_link omitted
	})
	if !errors.Is(err, templates.ErrMissingVariable) {
		t.Fatalf("Render(missing var) = %v, want ErrMissingVariable", err)
	}
}

// Scenario: P2e / 2 — Renderer is safe for concurrent use; cache double-check
// holds under load.
func TestRenderer_ConcurrentRenders(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	var wg sync.WaitGroup
	const N = 50
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			locale := "en"
			if i%2 == 0 {
				locale = "zh-CN"
			}
			_, err := r.Render(templates.TemplateEmailVerification, locale, map[string]string{
				"token":             "T",
				"verification_link": "https://x.example/v",
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Render: %v", err)
	}
}

// Scenario: P2e / 2 — placeholder TODO markers are stripped from the subject
// (.txt comments) and remain only in the HTML body as harmless comments.
func TestRenderer_PlaceholderTodoMarkersNotRenderedAsSubject(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	locales := []string{"zh-CN", "ja", "ko", "es", "fr", "de", "pt", "ru", "ar"}
	for _, loc := range locales {
		got, err := r.Render(templates.TemplateEmailVerification, loc, map[string]string{
			"token":             "T",
			"verification_link": "https://x.example/v",
		})
		if err != nil {
			t.Errorf("Render(%s): %v", loc, err)
			continue
		}
		if strings.Contains(got.Subject, "TODO") || strings.HasPrefix(got.Subject, "#") {
			t.Errorf("%s subject leaked marker: %q", loc, got.Subject)
		}
		if strings.Contains(got.TextBody, "TODO: localize") {
			t.Errorf("%s text body leaked TODO marker", loc)
		}
	}
}
