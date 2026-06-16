// Story 2.7 AC7 (2.7-INT-030) — account-deletion template render + locale-parity
// tests. All 3 templates (requested/cancelled/completed) must render in all 10
// locales with their required variables without a missing-variable error, and
// the value-bearing variables must appear in the rendered body.
package templates_test

import (
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/notification-svc/internal/templates"
)

func TestAccountDeletionRequested_RendersAllLocales(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	vars := map[string]string{
		"display_name":        "Ada",
		"pending_deletion_at": "2026-07-16T09:00:00Z",
		"cancel_url":          "https://console.he-api.com/en/account/recovery",
	}
	for _, loc := range allLocales {
		loc := loc
		t.Run(loc, func(t *testing.T) {
			t.Parallel()
			out, err := r.Render(templates.TemplateAccountDeletionRequested, loc, vars)
			if err != nil {
				t.Fatalf("render %s: %v", loc, err)
			}
			if strings.TrimSpace(out.Subject) == "" || strings.TrimSpace(out.TextBody) == "" {
				t.Errorf("%s: empty subject/body", loc)
			}
			if !strings.Contains(out.TextBody, "2026-07-16T09:00:00Z") {
				t.Errorf("%s: pending_deletion_at not in body", loc)
			}
		})
	}
}

func TestAccountDeletionCancelled_RendersAllLocales(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	for _, loc := range allLocales {
		loc := loc
		t.Run(loc, func(t *testing.T) {
			t.Parallel()
			out, err := r.Render(templates.TemplateAccountDeletionCancelled, loc, map[string]string{})
			if err != nil {
				t.Fatalf("render %s: %v", loc, err)
			}
			if strings.TrimSpace(out.Subject) == "" || strings.TrimSpace(out.TextBody) == "" {
				t.Errorf("%s: empty subject/body", loc)
			}
		})
	}
}

func TestAccountDeletionCompleted_RendersAllLocales(t *testing.T) {
	t.Parallel()
	r := templates.NewRenderer()
	vars := map[string]string{"executed_at": "2026-07-16T02:00:05Z"}
	for _, loc := range allLocales {
		loc := loc
		t.Run(loc, func(t *testing.T) {
			t.Parallel()
			out, err := r.Render(templates.TemplateAccountDeletionCompleted, loc, vars)
			if err != nil {
				t.Fatalf("render %s: %v", loc, err)
			}
			if !strings.Contains(out.TextBody, "2026-07-16T02:00:05Z") {
				t.Errorf("%s: executed_at not in body", loc)
			}
		})
	}
}
