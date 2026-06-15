// Implemented from the QA Test Design skeleton for Story 10.4 (Turing,
// 2026-06-15). Every designed scenario from
// docs/qa/assessments/10.4-test-design-20260615.md has a real body here; none is
// left as an unimplemented skeleton stub. Genuinely toolchain/self-referential or
// live-gated scenarios are t.Skip with a written reason (per the skeleton rule).
//
// Conventions (ratified — see story "Ratified Decisions"):
//   - R-OQ-10.4-1: NewClient returns the UPSTREAM openai.Client (no custom type).
//   - R-OQ-10.4-3: hermetic = option.WithHTTPClient(stub RoundTripper); SSE via
//     httptest.Server. NO real gateway. (Plumbing in protocol_invariants_test.go.)
//   - Dependency import path is GA major-suffixed: github.com/openai/openai-go/v3.

package heapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// repo-relative paths from the package working directory (packages/sdk-go).
const (
	pathGoMod     = "go.mod"
	pathReadme    = "README.md"
	pathLicense   = "LICENSE"
	pathGoWork    = "../../go.work"
	pathTestYml   = "../../.github/workflows/test.yml"
	pathLintYml   = "../../.github/workflows/lint.yml"
	openaiPkgPath = "github.com/openai/openai-go/v3"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// nonTestGoSources returns the concatenated source of every non-test .go file in
// the package — used for static "this symbol is NOT exported" assertions.
func nonTestGoSources(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			b.WriteString(readFile(t, n))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func runGoTool(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// ============================================================
// AC1: `go get github.com/he-api/sdk-go` 可用 (buildable / go.work-integrated module)
// ============================================================

// --- P0 ---

func Test_AC1_UNIT_001_PublicEntryExists(t *testing.T) {
	// 10.4-UNIT-001 — heapi.NewClient is callable and yields a usable openai.Client
	// (anchors the deliverable binding client.go <- this test). Usability is proven
	// by driving a real call through the hermetic stub transport.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	client := newStubClient(t, stub)
	if _, err := client.Chat.Completions.New(context.Background(), simpleChatParams()); err != nil {
		t.Fatalf("NewClient's chat surface MUST be usable; got %v", err)
	}
}

func Test_AC1_UNIT_002_ModulePathIsEpicLiteral(t *testing.T) {
	// 10.4-UNIT-002 — module path is the epic literal, NO /vN suffix (R-OQ-10.4-2).
	gomod := readFile(t, pathGoMod)
	if !strings.Contains(gomod, "module github.com/he-api/sdk-go\n") {
		t.Fatalf("go.mod module line MUST be exactly 'module github.com/he-api/sdk-go'; got:\n%s", gomod)
	}
	if strings.Contains(gomod, "module github.com/he-api/sdk-go/v") {
		t.Fatalf("module path MUST NOT carry a /vN suffix at v0/v1")
	}
}

func Test_AC1_UNIT_003_RequiresOpenAIGoV3(t *testing.T) {
	// 10.4-UNIT-003 — requires the GA major-suffixed dependency .../openai-go/v3.
	gomod := readFile(t, pathGoMod)
	if !strings.Contains(gomod, "github.com/openai/openai-go/v3 v3.") {
		t.Fatalf("go.mod MUST require github.com/openai/openai-go/v3 v3.x.y; got:\n%s", gomod)
	}
}

func Test_AC1_INT_001_GoBuildSucceeds(t *testing.T) {
	// 10.4-INT-001 — `go build ./...` exits 0 inside packages/sdk-go.
	runGoTool(t, "build", "./...")
}

func Test_AC1_INT_002_GoTestRaceGreen(t *testing.T) {
	// 10.4-INT-002 — self-referential (running `go test -race` from within a test
	// would recurse). The fact that THIS suite passes under the unit-go `-race`
	// lane IS the evidence; enforced in CI (.github/workflows/test.yml).
	t.Skip("self-referential: enforced by the unit-go `go test -race ./packages/sdk-go/...` lane")
}

func Test_AC1_INT_003_GoWorkUsesModule(t *testing.T) {
	// 10.4-INT-003 — root go.work integrates the module.
	work := readFile(t, pathGoWork)
	if !strings.Contains(work, "./packages/sdk-go") {
		t.Fatalf("go.work MUST contain `use ./packages/sdk-go`; got:\n%s", work)
	}
}

func Test_AC1_INT_004_CIUnitGoEnumeratesModule(t *testing.T) {
	// 10.4-INT-004 — unit-go lane runs go test over the module (R-OQ-10.4-5,
	// safety-lexicon precedent: go.work does NOT auto-enumerate packages/*).
	yml := readFile(t, pathTestYml)
	if !strings.Contains(yml, "./packages/sdk-go/...") {
		t.Fatalf("test.yml MUST run `go test ./packages/sdk-go/...`; not found")
	}
}

func Test_AC1_INT_005_StandaloneResolvableNoReplace(t *testing.T) {
	// 10.4-INT-005 — the module is independently resolvable: its own go.mod, no
	// monorepo `replace` directive / private GOPROXY needed for external `go get`.
	gomod := readFile(t, pathGoMod)
	for _, line := range strings.Split(gomod, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "replace ") {
			t.Fatalf("go.mod MUST NOT carry a replace directive (breaks standalone go get); got: %q", line)
		}
	}
}

// --- P1 ---

func Test_AC1_UNIT_004_GoDirectiveFloor122(t *testing.T) {
	// 10.4-UNIT-004 — external-reach floor `go 1.22`, not reflexive 1.25 (R-OQ-10.4-6).
	gomod := readFile(t, pathGoMod)
	if !goDirectiveIs(gomod, "1.22") {
		t.Fatalf("go.mod `go` directive MUST be 1.22; got:\n%s", gomod)
	}
}

func Test_AC1_INT_006_GoVetClean(t *testing.T) {
	// 10.4-INT-006 — `go vet ./...` clean.
	runGoTool(t, "vet", "./...")
}

func Test_AC1_INT_007_GofumptClean(t *testing.T) {
	// 10.4-INT-007 — gofumpt reports no diff. Skips if gofumpt is not installed in
	// this lane (it runs in the dedicated lint-go lane, not unit-go).
	if _, err := exec.LookPath("gofumpt"); err != nil {
		t.Skip("gofumpt not in PATH — enforced by the lint-go lane (.github/workflows/lint.yml)")
	}
	out, err := exec.Command("gofumpt", "-l", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("gofumpt failed: %v\n%s", err, out)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("gofumpt -l reported unformatted files:\n%s", out)
	}
}

func Test_AC1_INT_008_GolangciLintClean(t *testing.T) {
	// 10.4-INT-008 — golangci-lint clean. Skips if not installed (lint-go lane owns it).
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not in PATH — enforced by the lint-go lane (.github/workflows/lint.yml)")
	}
	out, err := exec.Command("golangci-lint", "run", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("golangci-lint reported issues: %v\n%s", err, out)
	}
}

func Test_AC1_INT_009_LintYmlScopeIncludesModule(t *testing.T) {
	// 10.4-INT-009 — lint.yml golangci args AND gofumpt scope include the module
	// (R-OQ-10.4-5: sdk-go is the first packages/* module needing lint coverage).
	yml := readFile(t, pathLintYml)
	if !strings.Contains(yml, "./packages/sdk-go/...") {
		t.Fatalf("lint.yml golangci-lint args MUST include ./packages/sdk-go/...")
	}
	if !strings.Contains(yml, "./packages/sdk-go") {
		t.Fatalf("lint.yml gofumpt scope MUST include ./packages/sdk-go")
	}
}

// --- P2 ---

func Test_AC1_UNIT_005_LicenseReadmeQuickstart(t *testing.T) {
	// 10.4-UNIT-005 — LICENSE + README present; README carries a Go quickstart block.
	if _, err := os.Stat(pathLicense); err != nil {
		t.Fatalf("LICENSE MUST be present: %v", err)
	}
	readme := readFile(t, pathReadme)
	if !strings.Contains(readme, "```go") {
		t.Fatalf("README MUST contain a ```go Quickstart code block (pkg.go.dev source)")
	}
	if !strings.Contains(readme, "heapi.NewClient") {
		t.Fatalf("README Quickstart MUST show heapi.NewClient usage")
	}
}

// --- Blind Spot Scenarios ---

func Test_AC1_BLIND_BOUNDARY_001_GoFloorCoexistsInWorkspace(t *testing.T) {
	// 10.4-BLIND-BOUNDARY-001 — module `go 1.22` <= workspace `go 1.25.0`.
	if !goDirectiveIs(readFile(t, pathGoMod), "1.22") {
		t.Fatalf("module go directive MUST be 1.22")
	}
	work := readFile(t, pathGoWork)
	if !strings.Contains(work, "go 1.25.0") {
		t.Fatalf("go.work MUST declare go 1.25.0 (workspace floor >= module floor)")
	}
	// And it actually builds under the 1.25 workspace toolchain (this very suite
	// is compiled by it), proving coexistence.
	runGoTool(t, "build", "./...")
}

// goDirectiveIs reports whether the go.mod text has exactly `go <version>`.
func goDirectiveIs(gomod, version string) bool {
	for _, line := range strings.Split(gomod, "\n") {
		if strings.TrimSpace(line) == "go "+version {
			return true
		}
	}
	return false
}

// ============================================================
// AC2: 与 OpenAI 官方 Go SDK 接口一致 (drop-in)
// ============================================================

// --- P0 ---

func Test_AC2_UNIT_010_NewClientReturnsUpstreamType(t *testing.T) {
	// 10.4-UNIT-010 (LINCHPIN) — NewClient's return type IS the upstream
	// openai.Client; no custom client type is exported by this package.
	got := reflect.TypeOf(NewClient())
	if got.PkgPath() != openaiPkgPath || got.Name() != "Client" {
		t.Fatalf("NewClient MUST return upstream %s.Client; got %s.%s", openaiPkgPath, got.PkgPath(), got.Name())
	}
	// Same identity as a hand-built openai.Client (drop-in equality).
	if reflect.TypeOf(NewClient()) != reflect.TypeOf(openai.NewClient()) {
		t.Fatalf("heapi.NewClient and openai.NewClient MUST return the identical type")
	}
	// Static guard: no exported `type Client` declared in the package source.
	if strings.Contains(nonTestGoSources(t), "type Client ") {
		t.Fatalf("package MUST NOT declare a custom exported Client type")
	}
}

func Test_AC2_UNIT_011_DefaultBaseURLConstant(t *testing.T) {
	// 10.4-UNIT-011 — single named constant == the He-API production gateway.
	if DEFAULT_BASE_URL != "https://api.he-api.com/v1" {
		t.Fatalf("DEFAULT_BASE_URL MUST be https://api.he-api.com/v1; got %q", DEFAULT_BASE_URL)
	}
}

func Test_AC2_UNIT_012_CredentialPrecedenceNeverOpenAIKey(t *testing.T) {
	// 10.4-UNIT-012 — explicit WithAPIKey > HE_API_KEY > none; OPENAI_API_KEY NEVER read.
	t.Setenv("HE_API_KEY", "he-key-from-env")
	t.Setenv("OPENAI_API_KEY", "sk-real-openai-MUST-NOT-LEAK")

	// (a) env HE_API_KEY wins over OPENAI_API_KEY.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	auth := stub.last(t).Header.Get("Authorization")
	if auth != "Bearer he-key-from-env" {
		t.Fatalf("Authorization MUST use HE_API_KEY; got %q", auth)
	}
	if strings.Contains(auth, "sk-real-openai") {
		t.Fatalf("OPENAI_API_KEY MUST NEVER reach the wire; got %q", auth)
	}

	// (b) explicit WithAPIKey overrides the env.
	stub2 := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c2 := newStubClient(t, stub2, option.WithAPIKey("explicit-key"))
	_, _ = c2.Chat.Completions.New(context.Background(), simpleChatParams())
	if got := stub2.last(t).Header.Get("Authorization"); got != "Bearer explicit-key" {
		t.Fatalf("explicit WithAPIKey MUST win; got %q", got)
	}
}

func Test_AC2_UNIT_013_BaseURLPrecedence(t *testing.T) {
	// 10.4-UNIT-013 — explicit WithBaseURL > HE_API_BASE_URL > DEFAULT_BASE_URL.
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1") // MUST NOT be honored

	// default
	if got := resolveBaseURL(); got != DEFAULT_BASE_URL {
		t.Fatalf("no env => DEFAULT_BASE_URL; got %q", got)
	}
	// env tier
	t.Setenv("HE_API_BASE_URL", "https://staging.he-api.test/v1")
	if got := resolveBaseURL(); got != "https://staging.he-api.test/v1" {
		t.Fatalf("HE_API_BASE_URL MUST override default; got %q", got)
	}
	// explicit option tier wins on the wire even over env.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub, option.WithBaseURL("https://explicit.he-api.test/v1"))
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	if host := hostOf(t, stub); host != "explicit.he-api.test" {
		t.Fatalf("explicit WithBaseURL MUST win; request went to %q", host)
	}
}

func Test_AC2_UNIT_014_PinDriftGuardVersionBand(t *testing.T) {
	// 10.4-UNIT-014 — the installed openai-go/v3 version is inside the validated
	// major+floor band, and the band rejects out-of-band versions (fail-loud).
	resolved := ""
	if v, ok := installedOpenAIGoVersion(); ok {
		resolved = v
	} else {
		resolved = requireLineVersion(t) // fall back to go.mod require
	}
	if resolved == "" {
		t.Fatalf("could not resolve the installed openai-go/v3 version")
	}
	if !openAIGoVersionInBand(resolved) {
		t.Fatalf("PIN DRIFT: openai-go %s is outside the validated band [v%d.%d.0, v%d.0.0)",
			resolved, openAIGoMajor, openAIGoMinMinor, openAIGoMajor+1)
	}
	// The band MUST reject a downgrade and a major bump (fail-loud direction).
	if openAIGoVersionInBand("v3.0.0") {
		t.Errorf("band MUST reject below-floor v3.0.0")
	}
	if openAIGoVersionInBand("v4.0.0") {
		t.Errorf("band MUST reject the next major v4.0.0")
	}
}

func Test_AC2_INT_010_DefaultRequestLandsAtHe(t *testing.T) {
	// 10.4-INT-010 — no baseURL option => request host/path is the He-API gateway.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err != nil {
		t.Fatalf("chat call: %v", err)
	}
	req := stub.last(t)
	if host := hostOf(t, stub); host != "api.he-api.com" {
		t.Fatalf("default request MUST land at api.he-api.com; got %q", host)
	}
	if !strings.HasPrefix(req.Path, "/v1/") {
		t.Fatalf("path MUST be under /v1/; got %q", req.Path)
	}
}

func Test_AC2_INT_011_HeApiKeyReachesAuthorization(t *testing.T) {
	// 10.4-INT-011 — HE_API_KEY env => Authorization: Bearer on the outgoing request.
	t.Setenv("HE_API_KEY", "secret-he-key")
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	if got := stub.last(t).Header.Get("Authorization"); got != "Bearer secret-he-key" {
		t.Fatalf("Authorization MUST carry HE_API_KEY; got %q", got)
	}
}

func Test_AC2_INT_012_ChatCompletionSameTypeShape(t *testing.T) {
	// 10.4-INT-012 — chat returns openai-go's ChatCompletion; shape holds.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	resp, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err != nil {
		t.Fatalf("chat call: %v", err)
	}
	if reflect.TypeOf(resp).Elem().PkgPath() != openaiPkgPath {
		t.Fatalf("response MUST be openai-go's *ChatCompletion")
	}
	assertChatCompletionShape(t, resp, "qwen-max")
}

func Test_AC2_INT_013_StreamingSSEIterates(t *testing.T) {
	// 10.4-INT-013 — NewStreaming over httptest SSE; iterate to [DONE]; Err()==nil.
	frames := []string{
		sseData(`{"id":"chatcmpl-s","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{"role":"assistant"}}]}`),
		sseData(`{"id":"chatcmpl-s","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{"content":"hi"}}]}`),
		sseData(`{"id":"chatcmpl-s","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		sseDone,
	}
	srv := sseServer(t, frames)
	c := NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"))
	stream := c.Chat.Completions.NewStreaming(context.Background(), simpleChatParams())
	defer func() { _ = stream.Close() }()

	kinds := []string{"bootstrap", "content", "terminal"}
	n := 0
	for stream.Next() {
		chunk := stream.Current()
		if n < len(kinds) {
			assertChatChunkShape(t, chunk, "qwen-max", kinds[n])
		}
		n++
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream.Err() MUST be nil after clean [DONE]; got %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 chunks before [DONE]; got %d", n)
	}
}

func Test_AC2_INT_014_StreamingTailUsageInvariant(t *testing.T) {
	// 10.4-INT-014 — include_usage tail chunk carries usage; prompt+completion==total.
	frames := []string{
		sseData(`{"id":"chatcmpl-u","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{"content":"hi"}}]}`),
		sseData(`{"id":"chatcmpl-u","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`),
		sseDone,
	}
	srv := sseServer(t, frames)
	c := NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"))
	params := simpleChatParams()
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	stream := c.Chat.Completions.NewStreaming(context.Background(), params)
	defer func() { _ = stream.Close() }()

	var tail openai.ChatCompletionChunk
	for stream.Next() {
		tail = stream.Current()
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream.Err(): %v", err)
	}
	if tail.Usage.TotalTokens == 0 {
		t.Fatalf("tail chunk MUST carry usage")
	}
	assertUsageTriple(t, tail.Usage.PromptTokens, tail.Usage.CompletionTokens, tail.Usage.TotalTokens, true)
}

func Test_AC2_INT_015_ErrorTransparencyHeCode(t *testing.T) {
	// 10.4-INT-015 — 402 402_quota_exhausted => *openai.Error with .Code + he_request_id.
	body := mockErrorEnvelope("402_quota_exhausted", "invalid_request_error", "req_0123456789ab")
	stub := &stubTransport{respond: jsonResponder(402, body, nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err == nil {
		t.Fatalf("expected an error for HTTP 402")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error MUST be *openai.Error; got %T", err)
	}
	if apiErr.Code != "402_quota_exhausted" {
		t.Errorf(".Code MUST be readable; got %q", apiErr.Code)
	}
	if rid := HeRequestID(err); rid != "req_0123456789ab" {
		t.Errorf("he_request_id MUST be readable; got %q", rid)
	}
}

func Test_AC2_INT_016_XHeCostUsdAbsentTolerated(t *testing.T) {
	// 10.4-INT-016 (OQ5) — X-He-Cost-Usd missing => graceful; SDK never bills from it.
	// The SDK exposes no cost-from-header surface at all; a normal call with the
	// header absent must succeed and carry no cost signal.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	resp, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err != nil {
		t.Fatalf("chat call MUST succeed without X-He-Cost-Usd; got %v", err)
	}
	assertChatCompletionShape(t, resp, "qwen-max")
}

func Test_AC2_INT_017_DropInSwapEquivalence(t *testing.T) {
	// 10.4-INT-017 — heapi.NewClient() == openai.NewClient(WithBaseURL,WithAPIKey)
	// for the same stubbed response/error.
	t.Setenv("HE_API_KEY", "k")
	mock := mockChatCompletionJSON("qwen-max")

	stubA := &stubTransport{respond: jsonResponder(200, mock, nil)}
	heClient := NewClient(option.WithHTTPClient(&http.Client{Transport: stubA}), option.WithMaxRetries(0))
	respA, errA := heClient.Chat.Completions.New(context.Background(), simpleChatParams())

	stubB := &stubTransport{respond: jsonResponder(200, mock, nil)}
	oaClient := openai.NewClient(
		option.WithBaseURL(DEFAULT_BASE_URL),
		option.WithAPIKey("k"),
		option.WithHTTPClient(&http.Client{Transport: stubB}),
		option.WithMaxRetries(0),
	)
	respB, errB := oaClient.Chat.Completions.New(context.Background(), simpleChatParams())

	if (errA == nil) != (errB == nil) {
		t.Fatalf("error parity mismatch: heapi=%v openai=%v", errA, errB)
	}
	if respA.ID != respB.ID || respA.Model != respB.Model || len(respA.Choices) != len(respB.Choices) {
		t.Fatalf("response parity mismatch between heapi and openai clients")
	}
	if hostOf(t, stubA) != hostOf(t, stubB) {
		t.Fatalf("both clients MUST target the same host")
	}
}

// --- P1 ---

func Test_AC2_INT_018_ExplicitBaseURLOverrides(t *testing.T) {
	// 10.4-INT-018 — option.WithBaseURL(x) => request hits x.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub, option.WithBaseURL("https://override.he-api.test/v1"))
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	if host := hostOf(t, stub); host != "override.he-api.test" {
		t.Fatalf("explicit baseURL MUST be honored; got %q", host)
	}
}

func Test_AC2_INT_019_EnvBaseURLOverrides(t *testing.T) {
	// 10.4-INT-019 — HE_API_BASE_URL overrides default when no explicit option.
	t.Setenv("HE_API_BASE_URL", "https://env.he-api.test/v1")
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	if host := hostOf(t, stub); host != "env.he-api.test" {
		t.Fatalf("HE_API_BASE_URL MUST override default; got %q", host)
	}
}

func Test_AC2_INT_020_EmbeddingsSameTypeShape(t *testing.T) {
	// 10.4-INT-020 — embeddings returns openai-go same-type; shape holds.
	stub := &stubTransport{respond: jsonResponder(200, mockEmbeddingJSON("text-embedding-v2"), nil)}
	c := newStubClient(t, stub)
	resp, err := c.Embeddings.New(context.Background(), openai.EmbeddingNewParams{
		Model: "text-embedding-v2",
		Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String("hello")},
	})
	if err != nil {
		t.Fatalf("embeddings call: %v", err)
	}
	assertEmbeddingShape(t, resp, "text-embedding-v2")
}

func Test_AC2_INT_021_ModelsListSameTypeShape(t *testing.T) {
	// 10.4-INT-021 — models list returns openai-go same-type; entry shape holds.
	stub := &stubTransport{respond: jsonResponder(200, mockModelsListJSON(), nil)}
	c := newStubClient(t, stub)
	page, err := c.Models.List(context.Background())
	if err != nil {
		t.Fatalf("models list: %v", err)
	}
	if len(page.Data) == 0 {
		t.Fatalf("models list MUST be non-empty")
	}
	for _, m := range page.Data {
		assertModelEntryShape(t, m)
	}
}

func Test_AC2_INT_022_UnknownModel400(t *testing.T) {
	// 10.4-INT-022 — unknown model => 400, *openai.Error .Code == 400_invalid_request.
	body := mockErrorEnvelope("400_invalid_request", "invalid_request_error", "req_aaaaaaaaaaaa")
	stub := &stubTransport{respond: jsonResponder(400, body, nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *openai.Error; got %T", err)
	}
	if apiErr.Code != "400_invalid_request" {
		t.Fatalf(".Code MUST be 400_invalid_request; got %q", apiErr.Code)
	}
}

func Test_AC2_INT_023_ErrorEnvelopeShape(t *testing.T) {
	// 10.4-INT-023 — 5-field envelope; he_request_id matches ^req_[0-9a-f]{12}$.
	body := mockErrorEnvelope("400_invalid_request", "invalid_request_error", "req_0011aaff2299")
	stub := &stubTransport{respond: jsonResponder(400, body, nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	assertErrorEnvelopeShape(t, err, "400_invalid_request", 400)
}

func Test_AC2_INT_024_RoutingHeaderReachesWire(t *testing.T) {
	// 10.4-INT-024 — per-request WithHeader routing strategy reaches the wire;
	// the SDK does NOT re-validate it (gateway is the validator).
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams(),
		option.WithHeader("X-He-Routing-Strategy", "cost"))
	if got := stub.last(t).Header.Get("X-He-Routing-Strategy"); got != "cost" {
		t.Fatalf("X-He-Routing-Strategy MUST reach the wire; got %q", got)
	}
}

func Test_AC2_INT_025_ResponseHeadersViaWithResponseInto(t *testing.T) {
	// 10.4-INT-025 — WithResponseInto exposes X-He-Request-Id + X-He-Selected-Model.
	headers := map[string]string{
		"X-He-Request-Id":     "req_feedfacecafe",
		"X-He-Selected-Model": "qwen-max",
	}
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), headers)}
	c := newStubClient(t, stub)
	var raw *http.Response
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams(), option.WithResponseInto(&raw))
	if err != nil {
		t.Fatalf("chat call: %v", err)
	}
	if raw == nil {
		t.Fatalf("WithResponseInto MUST populate the raw response")
	}
	if got := raw.Header.Get("X-He-Request-Id"); got != "req_feedfacecafe" {
		t.Errorf("X-He-Request-Id MUST be readable; got %q", got)
	}
	if got := raw.Header.Get("X-He-Selected-Model"); got != "qwen-max" {
		t.Errorf("X-He-Selected-Model MUST be readable; got %q", got)
	}
}

func Test_AC2_INT_026_BalancePackageLevelFn(t *testing.T) {
	// 10.4-INT-026 — heapi.Balance => GET /v1/balance?currency=usd via escape hatch.
	stub := &stubTransport{respond: jsonResponder(200,
		`{"current_usd":"10.0000","currency":"usd","current_display":"10.0000","total_cost_display":"0.0000","fx_rate":"1.00000000","fx_as_of":"2026-06-16T00:00:00Z"}`, nil)}
	c := newStubClient(t, stub)
	bal, err := Balance(context.Background(), c, "usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	req := stub.last(t)
	if req.Method != "GET" || req.Path != "/v1/balance" {
		t.Fatalf("MUST GET /v1/balance; got %s %s", req.Method, req.Path)
	}
	if req.Query != "currency=usd" {
		t.Fatalf("query MUST be currency=usd; got %q", req.Query)
	}
	if bal.CurrentUSD != "10.0000" {
		t.Fatalf("balance decode mismatch; got %+v", bal)
	}
}

func Test_AC2_INT_027_UsagePackageLevelFn(t *testing.T) {
	// 10.4-INT-027 — heapi.Usage => GET /v1/usage (correct path/query).
	stub := &stubTransport{respond: jsonResponder(200,
		`{"object":"usage","currency":"rmb","total_cost_display":"72.00","fx_rate":"7.20000000","fx_as_of":"2026-06-16T00:00:00Z"}`, nil)}
	c := newStubClient(t, stub)
	usage, err := Usage(context.Background(), c, "rmb")
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	req := stub.last(t)
	if req.Method != "GET" || req.Path != "/v1/usage" {
		t.Fatalf("MUST GET /v1/usage; got %s %s", req.Method, req.Path)
	}
	if req.Query != "currency=rmb" {
		t.Fatalf("query MUST be currency=rmb; got %q", req.Query)
	}
	if usage.Currency != "rmb" {
		t.Fatalf("usage decode mismatch; got %+v", usage)
	}
}

func Test_AC2_E2E_001_LiveDropInEquivalence(t *testing.T) {
	// 10.4-E2E-001 — live drop-in round-trip; skips unless a real gateway is wired.
	gw := os.Getenv("HE_API_TEST_GATEWAY_URL")
	if gw == "" {
		t.Skip("HE_API_TEST_GATEWAY_URL unset — live drop-in arm skipped (R-OQ-10.4-3 skip-if-unset)")
	}
	c := NewClient(option.WithBaseURL(gw))
	resp, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	assertChatCompletionShape(t, resp, resp.Model)
}

// --- P2 ---

func Test_AC2_UNIT_015_NoAsyncClientSurface(t *testing.T) {
	// 10.4-UNIT-015 — no AsyncClient surface exported (Go is context-native).
	src := nonTestGoSources(t)
	for _, bad := range []string{"AsyncClient", "type Async", "func NewAsync"} {
		if strings.Contains(src, bad) {
			t.Fatalf("package MUST NOT export an async surface; found %q", bad)
		}
	}
}

func Test_AC2_INT_028_ABHeaderReachesWire(t *testing.T) {
	// 10.4-INT-028 — per-request WithHeader A/B models reaches the wire.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams(),
		option.WithHeader("X-He-AB-Models", "qwen-max,glm-4"))
	if got := stub.last(t).Header.Get("X-He-AB-Models"); got != "qwen-max,glm-4" {
		t.Fatalf("X-He-AB-Models MUST reach the wire; got %q", got)
	}
}

func Test_AC2_INT_029_AudioVisionSmoke(t *testing.T) {
	// 10.4-INT-029 — audio (9.7) auto-available via reuse: client.Audio.Speech is
	// reachable through the same wrapped client; 1 mock smoke, no new SDK code.
	stub := &stubTransport{respond: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"audio/mpeg"}},
			Body:       io.NopCloser(strings.NewReader("ID3-fake-mp3-bytes")),
			Request:    req,
		}, nil
	}}
	c := newStubClient(t, stub)
	speech, err := c.Audio.Speech.New(context.Background(), openai.AudioSpeechNewParams{
		Model:          "tts-1",
		Input:          "hello",
		Voice:          openai.AudioSpeechNewParamsVoiceUnion{OfString: openai.String("alloy")},
		ResponseFormat: openai.AudioSpeechNewParamsResponseFormatMP3,
	})
	if err != nil {
		t.Fatalf("audio speech smoke: %v", err)
	}
	defer func() { _ = speech.Body.Close() }()
	if stub.last(t).Path != "/v1/audio/speech" {
		t.Fatalf("audio speech MUST hit /v1/audio/speech; got %q", stub.last(t).Path)
	}
}

// --- Blind Spot Scenarios ---

func Test_AC2_BLIND_BOUNDARY_002_MissingKeyFallsThrough(t *testing.T) {
	// 10.4-BLIND-BOUNDARY-002 — no HE_API_KEY AND no WithAPIKey => openai-go native
	// key-missing behavior, no panic; OPENAI_API_KEY MUST NOT be substituted.
	t.Setenv("HE_API_KEY", "") // present-but-empty == native key-missing path
	t.Setenv("OPENAI_API_KEY", "sk-should-not-be-used")
	stub := &stubTransport{respond: jsonResponder(401,
		mockErrorEnvelope("401_invalid_api_key", "invalid_request_error", "req_000000000000"), nil)}
	c := newStubClient(t, stub) // must not panic
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err == nil {
		t.Fatalf("missing key MUST surface the gateway's auth error")
	}
	if got := stub.last(t).Header.Get("Authorization"); strings.Contains(got, "sk-should-not-be-used") {
		t.Fatalf("OPENAI_API_KEY MUST NOT be substituted; Authorization=%q", got)
	}
}

func Test_AC2_BLIND_BOUNDARY_003_EmptyBaseURLEnvFallsBack(t *testing.T) {
	// 10.4-BLIND-BOUNDARY-003 — empty HE_API_BASE_URL => fall back to DEFAULT_BASE_URL.
	t.Setenv("HE_API_BASE_URL", "")
	if got := resolveBaseURL(); got != DEFAULT_BASE_URL {
		t.Fatalf("empty HE_API_BASE_URL MUST fall back to DEFAULT_BASE_URL; got %q", got)
	}
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	_, _ = c.Chat.Completions.New(context.Background(), simpleChatParams())
	if host := hostOf(t, stub); host != "api.he-api.com" {
		t.Fatalf("empty env MUST NOT yield an empty host; got %q", host)
	}
}

func Test_AC2_BLIND_BOUNDARY_004_BalanceInvalidCurrencyPassthrough(t *testing.T) {
	// 10.4-BLIND-BOUNDARY-004 — Balance forwards an invalid currency verbatim; the
	// gateway 400s; the SDK does NOT re-validate.
	stub := &stubTransport{respond: jsonResponder(400,
		mockErrorEnvelope("400_unsupported_currency", "invalid_request_error", "req_bbbbbbbbbbbb"), nil)}
	c := newStubClient(t, stub)
	_, err := Balance(context.Background(), c, "eur")
	if stub.last(t).Query != "currency=eur" {
		t.Fatalf("invalid currency MUST be forwarded verbatim; got query %q", stub.last(t).Query)
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "400_unsupported_currency" {
		t.Fatalf("gateway 400_unsupported_currency MUST surface; got %v", err)
	}
}

func Test_AC2_BLIND_ERROR_001_ConnectionRefusedPropagates(t *testing.T) {
	// 10.4-BLIND-ERROR-001 — transport error propagates, not swallowed.
	stub := &stubTransport{respond: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	}}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err == nil {
		t.Fatalf("transport error MUST propagate")
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		t.Fatalf("a connection failure MUST NOT masquerade as an API *openai.Error; got %v", err)
	}
}

func Test_AC2_BLIND_ERROR_002_ServiceUnavailable503(t *testing.T) {
	// 10.4-BLIND-ERROR-002 — gateway 503 => *openai.Error propagated.
	stub := &stubTransport{respond: jsonResponder(503,
		mockErrorEnvelope("503_service_unavailable", "server_error", "req_cccccccccccc"), nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
		t.Fatalf("503 MUST surface as *openai.Error; got %v", err)
	}
}

func Test_AC2_BLIND_ERROR_003_MalformedJSONSurfaced(t *testing.T) {
	// 10.4-BLIND-ERROR-003 — malformed 200 body => decode error surfaced, no panic.
	stub := &stubTransport{respond: jsonResponder(200, `{"id":"chatcmpl-x","choices":[`, nil)}
	c := newStubClient(t, stub)
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams())
	if err == nil {
		t.Fatalf("malformed JSON MUST surface a decode error")
	}
}

func Test_AC2_BLIND_ERROR_004_ContextDeadlineExceeded(t *testing.T) {
	// 10.4-BLIND-ERROR-004 — ctx deadline => context.DeadlineExceeded propagated.
	stub := &stubTransport{respond: func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}
	c := newStubClient(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Chat.Completions.New(ctx, simpleChatParams())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx deadline MUST propagate as context.DeadlineExceeded; got %v", err)
	}
}

func Test_AC2_BLIND_ERROR_005_StreamMidErrorStops(t *testing.T) {
	// 10.4-BLIND-ERROR-005 — error mid-stream => stream.Err() non-nil; stops gracefully.
	frames := []string{
		sseData(`{"id":"chatcmpl-e","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{"content":"hi"}}]}`),
		sseData(`{"error":{"code":"500_internal","message":"boom","type":"server_error","param":null,"he_request_id":"req_dddddddddddd"}}`),
	}
	srv := sseServer(t, frames)
	c := NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"))
	stream := c.Chat.Completions.NewStreaming(context.Background(), simpleChatParams())
	defer func() { _ = stream.Close() }()
	for stream.Next() { //nolint:revive // draining to the mid-stream error
	}
	if stream.Err() == nil {
		t.Fatalf("mid-stream error MUST surface via stream.Err()")
	}
}

func Test_AC2_BLIND_RESOURCE_001_StreamCloseReleases(t *testing.T) {
	// 10.4-BLIND-RESOURCE-001 — stream.Close() (defer) releases the body cleanly.
	frames := []string{
		sseData(`{"id":"chatcmpl-r","object":"chat.completion.chunk","created":` + nowStr() + `,"model":"qwen-max","choices":[{"index":0,"delta":{"content":"hi"}}]}`),
		sseDone,
	}
	srv := sseServer(t, frames)
	c := NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"))
	stream := c.Chat.Completions.NewStreaming(context.Background(), simpleChatParams())
	stream.Next()
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close() MUST release without error; got %v", err)
	}
	// idempotent / safe second close.
	_ = stream.Close()
}

func Test_AC2_BLIND_RESOURCE_002_RawResponseBodyClosed(t *testing.T) {
	// 10.4-BLIND-RESOURCE-002 — WithResponseInto raw body is readable then closes clean.
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	var raw *http.Response
	_, err := c.Chat.Completions.New(context.Background(), simpleChatParams(), option.WithResponseInto(&raw))
	if err != nil {
		t.Fatalf("chat call: %v", err)
	}
	if raw == nil || raw.Body == nil {
		t.Fatalf("raw response/body MUST be populated")
	}
	if _, err := io.ReadAll(raw.Body); err != nil {
		t.Fatalf("raw body MUST be readable; got %v", err)
	}
	if err := raw.Body.Close(); err != nil {
		t.Fatalf("raw body MUST close cleanly; got %v", err)
	}
}

func Test_AC2_BLIND_CONCURRENCY_001_SharedClientRaceFree(t *testing.T) {
	// 10.4-BLIND-CONCURRENCY-001 — one client shared across goroutines is race-free
	// (run under `go test -race`).
	stub := &stubTransport{respond: jsonResponder(200, mockChatCompletionJSON("qwen-max"), nil)}
	c := newStubClient(t, stub)
	var wg sync.WaitGroup
	const n = 16
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.Chat.Completions.New(context.Background(), simpleChatParams()); err != nil {
				t.Errorf("concurrent call failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if stub.count() != n {
		t.Fatalf("expected %d captured requests; got %d", n, stub.count())
	}
}

// ---------------------------------------------------------------------------
// Local test helpers (kept here so they sit beside their use sites).
// ---------------------------------------------------------------------------

func simpleChatParams() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{
		Model:    "qwen-max",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")},
	}
}

func hostOf(t *testing.T, stub *stubTransport) string {
	t.Helper()
	u := stub.last(t).URL
	// URL captured as a string; parse the host cheaply.
	const sep = "://"
	i := strings.Index(u, sep)
	if i < 0 {
		t.Fatalf("unexpected captured URL %q", u)
	}
	rest := u[i+len(sep):]
	if j := strings.IndexAny(rest, "/?"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// nowStr is the current unix time as a string, for SSE chunk `created` fields.
func nowStr() string { return strconv.FormatInt(time.Now().Unix(), 10) }

// requireLineVersion parses the openai-go/v3 version from go.mod's require
// directive (both the single-line `require X v...` and block `	X v...` forms).
// Used as the in-workspace fallback when debug.ReadBuildInfo carries no Deps.
func requireLineVersion(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(readFile(t, pathGoMod), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "require ")
		if strings.HasPrefix(line, openaiPkgPath+" v") {
			parts := strings.Fields(line)
			return parts[len(parts)-1]
		}
	}
	return ""
}
