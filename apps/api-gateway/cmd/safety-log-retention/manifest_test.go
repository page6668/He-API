package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	cronTemplatePath = "../../../../infra/helm/api-gateway/templates/cronjob-safety-log-retention.yaml"
	chartValuesPath  = "../../../../infra/helm/api-gateway/values.yaml"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(p))
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// 8.5-MANIFEST-001 / 8.5-BLIND-CONCURRENCY-002 — the retention CronJob uses a
// K8s-valid 5-field schedule + `.spec.timeZone` (Architect Medium fix), runs with
// concurrencyPolicy: Forbid (no overlapping sweeps) and backoffLimit: 0
// (human-verified on failure). The schedule LITERAL lives in values.yaml.
func Test8_5_MANIFEST001_CronJobShape(t *testing.T) {
	tmpl := readFile(t, cronTemplatePath)
	vals := readFile(t, chartValuesPath)

	if !strings.Contains(tmpl, "kind: CronJob") {
		t.Fatal("template must define a CronJob")
	}
	if !strings.Contains(tmpl, "timeZone:") {
		t.Fatal("CronJob must set .spec.timeZone (k8s 1.27+), not a 6-field schedule")
	}
	for _, want := range []string{
		"concurrencyPolicy: {{ .Values.safetyLogRetention.concurrencyPolicy }}",
		"backoffLimit: {{ .Values.safetyLogRetention.backoffLimit }}",
		"safety-log-retention",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("template missing %q", want)
		}
	}

	// The schedule literal must be a valid 5-field cron (no trailing tz token).
	m := regexp.MustCompile(`schedule:\s*"([^"]+)"`).FindStringSubmatch(vals)
	if m == nil {
		t.Fatal("values.yaml: safetyLogRetention.schedule literal not found")
	}
	if fields := strings.Fields(m[1]); len(fields) != 5 {
		t.Fatalf("schedule %q must be 5-field k8s cron, got %d fields", m[1], len(fields))
	}
	for _, want := range []string{
		`concurrencyPolicy: Forbid`,
		`backoffLimit: 0`,
		`timeZone:`,
	} {
		if !strings.Contains(vals, want) {
			t.Errorf("values.yaml missing %q", want)
		}
	}
}
