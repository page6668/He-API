// Story 3.1 — AC3 doc-ratification tests.
//
// All assertions use os.ReadFile + strings/regexp rather than shelling out to
// grep, keeping the suite hermetic and avoiding platform-specific grep flags.
// The repo root is computed by walking up from the test file's directory until
// a `go.work` is found (workspace marker — stable across check-outs).
//
// Wright (Architect) MJ-2 scope: ratification covers high-level-architecture
// + tech-stack + epic-3 YAML AND the monolithic architecture.md + prd.md +
// perf §10 + architecture index.md + epics.md catalog (5 additional files).
// docs/stories/*.md are left alone (historical annotations).
package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("repo root (go.work) not found walking upward")
	return ""
}

func readFile(t *testing.T, abs string) string {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read %s: %v", abs, err)
	}
	return string(b)
}

// fiberish matches Fiber, fiber, or fasthttp (case-sensitive on substring,
// case-insensitive on the leading char so "Fiber" and "fiber" both hit).
var fiberish = regexp.MustCompile(`(?i)(Fiber|fasthttp)`)

// ratificationContextMarkers are phrases that mark a Fiber mention as
// historical / ratified-context, not a current-statement claim. A Fiber hit
// is "allowed" if EITHER its own line OR any of the preceding 6 lines contain
// one of these markers.
var ratificationContextMarkers = []string{
	"Original (deprecated",
	"deprecated 2026-05-18, Story 3.1 ratification",
	"Story 3.1",
	"feature gap",
	"adapter shim",
	"no longer load-bearing",
	"deferred to Epic 9",
	"k6 baseline",
	"从未落地实现",
	"Change Log",
	"ADR-2 history",
}

// assertFiberHitsAreContextual scans `body` (already read from `relPath`) and
// fails if any line containing Fiber/fasthttp is NOT under a ratification
// marker.
func assertFiberHitsAreContextual(t *testing.T, relPath, body string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !fiberish.MatchString(line) {
			continue
		}
		// Window: this line + 6 preceding lines.
		lo := i - 6
		if lo < 0 {
			lo = 0
		}
		window := strings.Join(lines[lo:i+1], "\n")
		var matched bool
		for _, m := range ratificationContextMarkers {
			if strings.Contains(window, m) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s:%d Fiber/fasthttp mention without ratification context: %q", relPath, i+1, line)
		}
	}
}

// ============================================================
// AC3: ADR-2 + tech-stack + Epic-3 YAML + 5-doc straggler set
// ============================================================

// Scenario: 3.1-UNIT-040
// Priority: P0 | BR: BR-3.4 (revised) / MJ-2
func TestDocRatification_grepDocsForFiberHasOnlyAnnotatedHits(t *testing.T) {
	root := repoRoot(t)
	docsDir := filepath.Join(root, "docs")

	allowedSubdirs := map[string]bool{
		"stories": true, // historical story annotations — leave as-is (Wright MJ-2 note)
		"qa":      true, // QA test-design references Fiber by name
		"dev":     true, // dev logs / story 3.1 mentions Fiber by name
	}

	err := filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		rel, err := filepath.Rel(docsDir, path)
		if err != nil {
			return err
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) > 1 && allowedSubdirs[parts[0]] {
			return nil
		}
		body := readFile(t, path)
		if fiberish.MatchString(body) {
			assertFiberHitsAreContextual(t, "docs/"+rel, body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs/: %v", err)
	}
}

// Scenario: 3.1-UNIT-041
// Priority: P0 | BR: BR-3.5
func TestDocRatification_goModHasNoFiberDeps(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"apps/api-gateway/go.mod", "apps/api-gateway/go.sum"} {
		body := readFile(t, filepath.Join(root, rel))
		if regexp.MustCompile(`(?i)(gofiber|fasthttp)`).MatchString(body) {
			t.Errorf("%s contains gofiber/fasthttp module reference (BR-3.5)", rel)
		}
	}
}

// Scenario: 3.1-UNIT-042
// Priority: P0 | BR: AC3
func TestDocRatification_adr2DecisionAndRationaleRewritten(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture/high-level-architecture.md"))
	for _, want := range []string{"stdlib net/http", "1.22+", "connectrpc/connect", "1.16+"} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR-2 rewrite missing required phrase: %q", want)
		}
	}
	for _, bullet := range []string{
		"ServeMux added method-aware routing",
		"connectrpc/connect requires `http.Handler`",
		"2.2 / 2.3 / 2.4 / 2.5 / 2.6",
		"Cold-start budget (≤ 1s, Story 3.1 AC2)",
	} {
		if !strings.Contains(body, bullet) {
			t.Errorf("ADR-2 rationale missing bullet: %q", bullet)
		}
	}
}

// Scenario: 3.1-UNIT-043
// Priority: P0 | BR: BR-3.2
func TestDocRatification_adr2HistorySubblockPreservesOriginal(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture/high-level-architecture.md"))
	if !strings.Contains(body, "Original (deprecated 2026-05-18, Story 3.1 ratification)") {
		t.Error("§1.3.1 ADR-2 history sub-block missing the canonical annotation phrase")
	}
	if !strings.Contains(body, "API Gateway 用 Go（Fiber 框架）") {
		t.Error("§1.3.1 ADR-2 history sub-block missing the verbatim Fiber phrase (BR-3.2)")
	}
}

// Scenario: 3.1-UNIT-044
// Priority: P0 | BR: AC3
func TestDocRatification_techStackRowRewritten(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture/tech-stack.md"))
	row := "| **网关框架** | Go stdlib net/http + connectrpc/connect | stdlib (Go 1.22+) / connectrpc 1.16+ | HTTP server (stdlib) + gRPC over HTTP/2 |"
	if !strings.Contains(body, row) {
		t.Errorf("tech-stack.md §2.1 row missing canonical form: %q", row)
	}
}

// Scenario: 3.1-UNIT-045
// Priority: P0 | BR: AC3
func TestDocRatification_epic3StoryTitleRewritten(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/prd/epic-3-gateway-core.yaml"))
	wanted := "title: 网关 HTTP 框架（Go net/http + connectrpc, ratify）+ /health + 冷启动基准"
	if !strings.Contains(body, wanted) {
		t.Errorf("epic-3 yaml stories[0].title missing canonical form")
	}
}

// Scenario: 3.1-UNIT-046
// Priority: P0 | BR: MJ-2
func TestDocRatification_monolithicArchitectureMdGrepClean(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture.md"))
	assertFiberHitsAreContextual(t, "docs/architecture.md", body)
}

// Scenario: 3.1-UNIT-047
// Priority: P0 | BR: MJ-2
func TestDocRatification_monolithicPrdMdGrepClean(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/prd.md"))
	assertFiberHitsAreContextual(t, "docs/prd.md", body)
}

// Scenario: 3.1-UNIT-048
// Priority: P0 | BR: MJ-2
func TestDocRatification_perfChapterRewrittenWithStdlibAnnotation(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture/10-性能与可扩展性performance-scalability.md"))
	for _, want := range []string{"stdlib net/http", "Story 3.1", "Epic 9", "k6 baseline"} {
		if !strings.Contains(body, want) {
			t.Errorf("perf §10 missing required phrase: %q", want)
		}
	}
}

// Scenario: 3.1-UNIT-049
// Priority: P0 | BR: MJ-2
func TestDocRatification_architectureIndexAnchorUpdated(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/architecture/index.md"))
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "ADR-2") {
			continue
		}
		// Anchor links live on the same line as the title in the index TOC.
		if strings.Contains(line, "](") && fiberish.MatchString(line) {
			t.Errorf("index.md ADR-2 line still contains 'fiber'/'Fiber' (broken anchor risk): %q", line)
		}
	}
}

// Scenario: 3.1-UNIT-050
// Priority: P0 | BR: MJ-2
func TestDocRatification_epicsCatalogRewritten(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/prd/epics.md"))
	if strings.Contains(body, "fiber/echo") {
		t.Error("docs/prd/epics.md still contains literal 'fiber/echo'")
	}
	if !strings.Contains(body, "Go net/http + connectrpc") {
		t.Error("docs/prd/epics.md missing the ratified phrase 'Go net/http + connectrpc'")
	}
}

// Scenario: 3.1-UNIT-051
// Priority: P1 | BR: BR-3.1 / BR-3.3
func TestDocRatification_allModifiedDocsCiteStoryAndDate(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"docs/architecture/high-level-architecture.md",
		"docs/architecture/tech-stack.md",
		"docs/architecture.md",
		"docs/prd.md",
		"docs/architecture/10-性能与可扩展性performance-scalability.md",
	} {
		body := readFile(t, filepath.Join(root, rel))
		if !strings.Contains(body, "Story 3.1") {
			t.Errorf("%s missing 'Story 3.1' citation (BR-3.1)", rel)
		}
		if !strings.Contains(body, "2026-05-18") {
			t.Errorf("%s missing '2026-05-18' citation (BR-3.3)", rel)
		}
	}
}

// Scenario: 3.1-UNIT-052
// Priority: P1 | BR: MJ-2 note
//
// Story-history files (notably 2.5) MUST retain their Fiber references —
// they are historical annotations of the moment of Architect review and
// should not be retroactively rewritten.
func TestDocRatification_storyHistoryAnnotationsPreserved(t *testing.T) {
	body := readFile(t, filepath.Join(repoRoot(t), "docs/stories/2.5-profile-management.md"))
	if !fiberish.MatchString(body) {
		t.Error("2.5 story should still contain Fiber/fiber/fasthttp historical annotations; got none")
	}
}

// Scenario: 3.1-UNIT-053
// Priority: P2 | BR: AC3 / BR-3.3
//
// Same-PR coherence: this assertion runs at CI / merge time, not in the local
// test loop. Skip when the worktree is not at a tip commit that bundles code +
// docs (we cannot easily detect that from inside the runner).
func TestDocRatification_sameCommitContainsCodeAndDocs(t *testing.T) {
	t.Skip("Scenario 3.1-UNIT-053 is asserted at merge-commit review time via `gh pr diff`, not in-suite.")
}

// ============================================================
// Blind Spot — empty-set assertion for BR-3.5
// ============================================================

// Scenario: 3.1-BLIND-BOUNDARY-006
// Priority: P1 | BR: BR-3.5 (overlaps with 3.1-UNIT-041 — implemented once)
func TestDocRatification_BLIND_BOUNDARY_006_emptySetGoModGrep(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"apps/api-gateway/go.mod", "apps/api-gateway/go.sum"} {
		path := filepath.Join(root, rel)
		body, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s: %v", rel, err)
		}
		if regexp.MustCompile(`(?i)(gofiber|fasthttp)`).MatchString(string(body)) {
			t.Errorf("empty-set violated: %s contains gofiber/fasthttp", rel)
		}
	}
}
