// Story 9.3 AC2 T2.5 — usage_log_export_ready template render + locale-parity
// tests. All 10 locales must render with the BR-EX-15 variables (signed_url,
// expires_at, format, row_count, display_name) without a missing-variable error.
package templates_test

import (
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/notification-svc/internal/templates"
)

var allLocales = []string{"en", "zh-CN", "ar", "de", "es", "fr", "ja", "ko", "pt", "ru"}

func exportVars() map[string]string {
	return map[string]string{
		"display_name": "Ada",
		"signed_url":   "https://oss.example/usage-log-exports/u/exp.csv?sig=abc",
		"expires_at":   "2026-06-12 12:00 UTC",
		"format":       "CSV",
		"row_count":    "1234",
	}
}

func TestUsageLogExportReady_RendersAllLocales(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	for _, loc := range allLocales {
		loc := loc
		t.Run(loc, func(t *testing.T) {
			t.Parallel()
			out, err := r.Render(templates.TemplateUsageLogExportReady, loc, exportVars())
			if err != nil {
				t.Fatalf("render %s: %v", loc, err)
			}
			if strings.TrimSpace(out.Subject) == "" {
				t.Errorf("%s: empty subject", loc)
			}
			// The signed URL belongs in the email body (it IS the delivery
			// channel, BR-EX-15) — assert it renders into the body.
			if !strings.Contains(out.HTMLBody, exportVars()["signed_url"]) {
				t.Errorf("%s: html body missing the download link", loc)
			}
			if !strings.Contains(out.TextBody, exportVars()["signed_url"]) {
				t.Errorf("%s: text body missing the download link", loc)
			}
		})
	}
}

// A missing required variable must surface as an error (Missingkey=error),
// never render a blank/`<no value>` link.
func TestUsageLogExportReady_MissingVar_Errors(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	vars := exportVars()
	delete(vars, "signed_url")
	if _, err := r.Render(templates.TemplateUsageLogExportReady, "en", vars); err == nil {
		t.Fatal("expected an error when signed_url is omitted")
	}
}
