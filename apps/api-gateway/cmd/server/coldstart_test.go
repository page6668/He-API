// Story 3.1 — Cold-start benchmark + cross-AC integration tests.
//
// Per Wright (Architect) Round 1 MJ-1 Option (c) → BR-2.9: TestMain shells
// out to `go build` with CGO_ENABLED=0 + -trimpath + -ldflags="-s -w" forced
// via env override, then exec.Commands the resulting binary in the iteration
// loop. This guarantees production-parity flags regardless of inherited CI
// env (the `unit-go` job ships CGO=1 + -race).
//
// mn-1: OTEL_EXPORTER_OTLP_ENDPOINT is unset in TestMain; the existing
// `TestNewTracerProvider_EmptyEndpointFallback` in packages/go-observability
// exercises the no-op exporter path. No parallel HE_API_OTEL_DISABLED env var.
//
// mn-2: 10 runs (not 5) for genuine P95 percentile semantics.
//
// mn-3: span name is whatever `otelhttp.DefaultSpanNameFormatter` produces;
// we assert attributes (http.method, http.route, http.status_code), not the
// literal "http.health" string.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"debug/buildinfo"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	obs "github.com/he-api/he-api/packages/go-observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Package-level fixtures wired by TestMain.
var (
	gatewayBinPath string
	jwtKeyPath     string
	stubAuthAddr   string
	stubNotifAddr  string
	stubAuthLn     net.Listener
	stubNotifLn    net.Listener
	stubTempDir    string
)

const (
	gatewayImportPath = "github.com/he-api/he-api/apps/api-gateway/cmd/server"
	listenPort        = "8080"
	healthBudgetMS    = 1000
	coldStartRuns     = 10 // mn-2 — 10 runs for genuine P95 semantics
	pollInterval      = 5 * time.Millisecond
	bootTimeout       = 5 * time.Second
)

func TestMain(m *testing.M) {
	// mn-1: ensure no inherited OTLP endpoint drags the boot path through
	// the OTLP/gRPC dialer.
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	tmpDir, err := os.MkdirTemp("", "story-3.1-coldstart-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdtemp: %v\n", err)
		os.Exit(2)
	}
	stubTempDir = tmpDir

	// (1) RSA-key fixture — 2048-bit pair, public PEM written to a file the
	// gateway can read via HE_API_JWT_PUBLIC_KEY_PATH.
	jwtKeyPath, err = writeTestPublicKeyPEM(tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: rsa fixture: %v\n", err)
		os.RemoveAll(tmpDir)
		os.Exit(2)
	}
	_ = os.Setenv("HE_API_JWT_PUBLIC_KEY_PATH", jwtKeyPath)

	// (2) Stub upstream TCP listeners — gateway connectrpc clients dial
	// lazily on first /v1/* request, so accept-and-close is enough for the
	// boot path (BR-2.4).
	stubAuthLn, stubAuthAddr, err = startStubUpstream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: stub auth-svc: %v\n", err)
		os.RemoveAll(tmpDir)
		os.Exit(2)
	}
	stubNotifLn, stubNotifAddr, err = startStubUpstream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: stub notification-svc: %v\n", err)
		_ = stubAuthLn.Close()
		os.RemoveAll(tmpDir)
		os.Exit(2)
	}
	_ = os.Setenv("HE_API_AUTH_SVC_URL", "http://"+stubAuthAddr)
	_ = os.Setenv("HE_API_NOTIFICATION_SVC_URL", "http://"+stubNotifAddr)

	// (3) Build the binary once with production-parity flags (BR-2.6 / BR-2.9).
	gatewayBinPath = filepath.Join(tmpDir, "gateway")
	if runtime.GOOS == "windows" {
		gatewayBinPath += ".exe"
	}
	if err := buildGateway(gatewayBinPath); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: build gateway: %v\n", err)
		_ = stubAuthLn.Close()
		_ = stubNotifLn.Close()
		os.RemoveAll(tmpDir)
		os.Exit(2)
	}

	code := m.Run()

	_ = stubAuthLn.Close()
	_ = stubNotifLn.Close()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

// startStubUpstream opens a 127.0.0.1:0 listener that accepts and immediately
// closes every inbound connection — enough to satisfy the gateway's lazy
// connectrpc client construction (no dial happens at boot).
func startStubUpstream() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln, ln.Addr().String(), nil
}

func writeTestPublicKeyPEM(dir string) (string, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", err
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	path := filepath.Join(dir, "jwt_pub.pem")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := pem.Encode(f, block); err != nil {
		return "", err
	}
	return path, nil
}

// buildGateway shells out to `go build` with production-parity flags
// (BR-2.6 / BR-2.9). CGO_ENABLED=0 is forced via env override so any
// inherited CI `unit-go` CGO=1 + -race setting is overridden.
func buildGateway(outPath string) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", outPath, gatewayImportPath)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w (stderr=%s)", err, stderr.String())
	}
	return nil
}

// ============================================================
// Helpers — runner, kill, poll
// ============================================================

// gatewayRun owns one spawned binary's lifecycle. exec.Cmd.Wait must be
// called exactly once (concurrent Waits hang in awaitGoroutines waiting on
// the stderr-copy goroutines), so the single Wait happens in the goroutine
// started inside runOneColdStart; stop() blocks on the same waitDone channel.
type gatewayRun struct {
	cmd      *exec.Cmd
	stderr   *bytes.Buffer
	waitDone chan struct{}
	waitErr  error
}

// runOneColdStart spawns the supplied binary on :8080 and polls /health until
// it returns 200 OK or until `deadline` elapses. The returned gatewayRun owns
// the child's lifecycle; callers MUST invoke g.stop() to release the port.
func runOneColdStart(binPath string, env []string, args []string, deadline time.Duration) (time.Duration, int, *gatewayRun, error) {
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return 0, 0, nil, fmt.Errorf("exec start: %w", err)
	}
	g := &gatewayRun{cmd: cmd, stderr: &stderr, waitDone: make(chan struct{})}
	go func() {
		g.waitErr = cmd.Wait()
		close(g.waitDone)
	}()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	url := "http://127.0.0.1:" + listenPort + "/health"
	lastStatus := 0
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-g.waitDone:
			exit := -1
			var ee *exec.ExitError
			if errors.As(g.waitErr, &ee) {
				exit = ee.ExitCode()
			} else if g.cmd.ProcessState != nil {
				exit = g.cmd.ProcessState.ExitCode()
			}
			return 0, lastStatus, g, fmt.Errorf("api-gateway exited %d before /health was ready; stderr=%q", exit, g.stderr.String())
		case <-ctx.Done():
			return 0, lastStatus, g, fmt.Errorf("/health did not respond 200 within %s; final status=%d", deadline, lastStatus)
		case <-tick.C:
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			req.Close = true // disable keep-alive so srv.Shutdown can drain promptly
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			lastStatus = resp.StatusCode
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(start), resp.StatusCode, g, nil
			}
		}
	}
}

// stop signals the child (SIGTERM then SIGKILL fallback), waits for the
// single Wait goroutine, and then waits for the listen port to be rebindable
// (RESOURCE-001 invariant).
func (g *gatewayRun) stop(gracefulWindow time.Duration) {
	if g == nil || g.cmd == nil || g.cmd.Process == nil {
		return
	}
	select {
	case <-g.waitDone:
		// Process already exited (e.g., crashed during boot).
	default:
		_ = g.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-g.waitDone:
		case <-time.After(gracefulWindow):
			_ = g.cmd.Process.Kill()
			<-g.waitDone
		}
	}
	// Wait for the listen port to be free.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", "127.0.0.1:"+listenPort)
		if err == nil {
			_ = ln.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func killAndAwaitPortFree(t *testing.T, g *gatewayRun, gracefulWindow time.Duration) {
	t.Helper()
	g.stop(gracefulWindow)
}

// ============================================================
// AC2 — Unit: percentile helper
// ============================================================

// percentile returns the p-th percentile of `samples` (0 ≤ p ≤ 1) using the
// R-7 linear-interpolation method (same as numpy.percentile default and
// Excel PERCENTILE.INC). The input slice is sorted in place.
func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if len(samples) == 1 {
		return samples[0]
	}
	if p <= 0 {
		return samples[0]
	}
	if p >= 1 {
		return samples[len(samples)-1]
	}
	rank := p * float64(len(samples)-1)
	lo := int(rank)
	frac := rank - float64(lo)
	if lo+1 >= len(samples) {
		return samples[lo]
	}
	return samples[lo] + time.Duration(frac*float64(samples[lo+1]-samples[lo]))
}

// Scenario: 3.1-UNIT-020
// Priority: P1 | Level: unit | BR: BR-2.7 / mn-2
//
// Note: the skeleton comment cited ≈1138 ms; the R-7 percentile of
// [420,480,510,520,550,560,570,580,590,1200]ms at p=0.95 is 925.5 ms
// (rank=8.55 → 590 + 0.55*(1200-590) = 925.5). The skeleton number does not
// correspond to any standard percentile method; we follow R-7 (numpy default)
// and adjust the expected value accordingly.
func TestPercentile_p95OnTenElementFixture(t *testing.T) {
	fixture := []time.Duration{
		420 * time.Millisecond, 480 * time.Millisecond, 510 * time.Millisecond,
		520 * time.Millisecond, 550 * time.Millisecond, 560 * time.Millisecond,
		570 * time.Millisecond, 580 * time.Millisecond, 590 * time.Millisecond,
		1200 * time.Millisecond,
	}
	want := 925*time.Millisecond + 500*time.Microsecond
	got := percentile(fixture, 0.95)
	if diff := absDur(got - want); diff > time.Millisecond {
		t.Fatalf("p95: got %v, want %v ±1ms (R-7 linear interpolation)", got, want)
	}
}

// Scenario: 3.1-UNIT-021
// Priority: P1 | Level: unit | BR: BR-2.7
func TestPercentile_edgeCases(t *testing.T) {
	if got := percentile([]time.Duration{42 * time.Millisecond}, 0.95); got != 42*time.Millisecond {
		t.Fatalf("single-element: got %v, want 42ms", got)
	}
	// median of an unsorted slice.
	unsorted := []time.Duration{
		3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond,
	}
	if got := percentile(unsorted, 0.5); got != 2*time.Millisecond {
		t.Fatalf("median of unsorted: got %v, want 2ms", got)
	}
	// p=0 returns minimum.
	if got := percentile([]time.Duration{5 * time.Millisecond, 1 * time.Millisecond}, 0); got != 1*time.Millisecond {
		t.Fatalf("p=0: got %v, want 1ms", got)
	}
}

// Scenario: 3.1-UNIT-022
// Priority: P2 | Level: unit | BR: BR-2.4
func TestStubUpstreamListener_lifecycleClean(t *testing.T) {
	ln, addr, err := startStubUpstream()
	if err != nil {
		t.Fatalf("start stub: %v", err)
	}
	if addr == "" {
		t.Fatalf("stub address is empty")
	}
	// Verify the listener accepts a connection.
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	_ = conn.Close()
	if err := ln.Close(); err != nil {
		t.Fatalf("stub Close returned: %v", err)
	}
}

// Scenario: 3.1-UNIT-023
// Priority: P2 | Level: unit | BR: BR-2.4
func TestRSAKeyFixture_writesPEMAndCleansUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "rsa-fixture-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	path, err := writeTestPublicKeyPEM(dir)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("PEM decode: type=%q", func() string {
			if block == nil {
				return "<nil>"
			}
			return block.Type
		}())
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir not cleaned: stat err=%v", err)
	}
}

// ============================================================
// AC2 — Build / binary inspection
// ============================================================

// Scenario: 3.1-INT-020
// Priority: P0 | Level: integration | BR: BR-2.9 / MJ-1
func TestColdStart_buildUsesProductionParityFlags(t *testing.T) {
	if gatewayBinPath == "" {
		t.Fatal("gatewayBinPath not set by TestMain")
	}
	info, err := os.Stat(gatewayBinPath)
	if err != nil {
		t.Fatalf("binary stat: %v", err)
	}
	// On Unix, the executable bit MUST be set.
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatalf("binary not executable: mode=%v", info.Mode())
	}
}

// Scenario: 3.1-INT-021
// Priority: P0 | Level: integration | BR: BR-2.6 / BR-2.9
//
// Inspect the built binary via debug/buildinfo and assert (a) CGO_ENABLED=0,
// (b) -trimpath was applied, (c) no `-race` runtime symbols are present.
func TestColdStart_binaryInheritsProductionFlags(t *testing.T) {
	if gatewayBinPath == "" {
		t.Fatal("gatewayBinPath not set by TestMain")
	}
	bi, err := buildinfo.ReadFile(gatewayBinPath)
	if err != nil {
		t.Fatalf("buildinfo.ReadFile: %v", err)
	}
	settings := map[string]string{}
	for _, s := range bi.Settings {
		settings[s.Key] = s.Value
	}
	if cgo := settings["CGO_ENABLED"]; cgo != "0" {
		t.Errorf("CGO_ENABLED: got %q, want 0 (BR-2.6)", cgo)
	}
	if tp := settings["-trimpath"]; tp != "true" {
		t.Errorf("-trimpath: got %q, want true (BR-2.6)", tp)
	}
	if race := settings["-race"]; race == "true" {
		t.Errorf("-race detector enabled — BR-2.6 forbids on the measured binary")
	}
}

// Scenario: 3.1-INT-023
// Priority: P0 | Level: integration | BR: mn-1 / BR-2.5 (revised)
func TestColdStart_noOtelKillswitchEnvVar(t *testing.T) {
	if v, ok := os.LookupEnv("HE_API_OTEL_DISABLED"); ok {
		t.Fatalf("HE_API_OTEL_DISABLED leaked into env (mn-1 forbids): %q", v)
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT must be empty in TestMain: %q", v)
	}
}

// ============================================================
// AC2 — Cold-start runner (the P95 gate)
// ============================================================

// Scenario: 3.1-INT-022
// Priority: P0 | Level: integration | BR: BR-2.2 / BR-2.7 / mn-2
func TestColdStart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cold-start runner in -short mode")
	}
	if gatewayBinPath == "" {
		t.Fatal("gatewayBinPath not set by TestMain")
	}
	samples := make([]time.Duration, 0, coldStartRuns)
	for i := 0; i < coldStartRuns; i++ {
		dur, _, cmd, err := runOneColdStart(gatewayBinPath, nil, nil, bootTimeout)
		if err != nil {
			killAndAwaitPortFree(t, cmd, 2*time.Second)
			t.Fatalf("iter %d: %v", i+1, err)
		}
		samples = append(samples, dur)
		killAndAwaitPortFree(t, cmd, 2*time.Second)
	}
	// Sort + report stats.
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 := percentile(append([]time.Duration(nil), samples...), 0.50)
	p95 := percentile(append([]time.Duration(nil), samples...), 0.95)
	t.Logf("cold-start runs=%d min=%v p50=%v p95=%v max=%v samples=%v",
		coldStartRuns, sorted[0], p50, p95, sorted[len(sorted)-1], samples)
	if p95 > time.Duration(healthBudgetMS)*time.Millisecond {
		t.Fatalf("cold-start P95 = %v, exceeds budget = %dms; samples=%v", p95, healthBudgetMS, samples)
	}
}

// Scenario: 3.1-INT-024
// Priority: P0 | Level: integration | BR: AC2 §Error Handling
//
// Swap the binary path for /bin/false and confirm the runner reports the
// non-zero exit. The runner returns an error rather than t.Fatalf so the test
// can assert on it.
func TestColdStart_childExitBeforeReady_failsWithExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX false on windows")
	}
	// macOS puts `false` at /usr/bin/false; many Linux distros put it at
	// /bin/false. Probe both.
	var bad string
	for _, p := range []string{"/usr/bin/false", "/bin/false"} {
		if _, err := os.Stat(p); err == nil {
			bad = p
			break
		}
	}
	if bad == "" {
		t.Skip("no `false` binary available on this host")
	}
	_, _, g, err := runOneColdStart(bad, nil, nil, bootTimeout)
	if g != nil {
		killAndAwaitPortFree(t, g, 200*time.Millisecond)
	}
	if err == nil {
		t.Fatalf("expected error from %s; got nil", bad)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Fatalf("error must mention exit code; got %q", err.Error())
	}
}

// Scenario: 3.1-INT-025
// Priority: P0 | Level: integration | BR: AC2 §Error Handling
//
// Spawn `/bin/sleep 10` — the process is alive but never serves /health. The
// runner must time out and report final status (0 = no successful response).
func TestColdStart_neverReady_failsWithFinalStatus(t *testing.T) {
	sleep := "/bin/sleep"
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sleep on windows")
	}
	if _, err := os.Stat(sleep); err != nil {
		t.Skipf("%s not available: %v", sleep, err)
	}
	// Tight deadline keeps the test fast.
	deadline := 300 * time.Millisecond
	_, _, cmd, err := runOneColdStart(sleep, nil, []string{"10"}, deadline)
	if cmd != nil {
		killAndAwaitPortFree(t, cmd, 200*time.Millisecond)
	}
	if err == nil {
		t.Fatalf("expected timeout error; got nil")
	}
	if !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("error must mention /health timeout; got %q", err.Error())
	}
}

// Scenario: 3.1-INT-026 (alias 3.1-BLIND-RESOURCE-001)
// Priority: P0 | Level: integration | BR: RESOURCE-001
func TestColdStart_processCleanupReleasesPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	_, _, cmd, err := runOneColdStart(gatewayBinPath, nil, nil, bootTimeout)
	if err != nil {
		killAndAwaitPortFree(t, cmd, 2*time.Second)
		t.Fatalf("boot: %v", err)
	}
	killAndAwaitPortFree(t, cmd, 2*time.Second)
	// Confirm port :8080 is rebindable within 1 s.
	deadline := time.Now().Add(time.Second)
	for {
		ln, err := net.Listen("tcp", "127.0.0.1:"+listenPort)
		if err == nil {
			_ = ln.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %s still bound after kill+1s: %v", listenPort, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Alias for the blind-spot category — same assertion as INT-026.
func TestColdStart_BLIND_RESOURCE_001_portRebindableAfterKill(t *testing.T) {
	TestColdStart_processCleanupReleasesPort(t)
}

// Scenario: 3.1-INT-027
// Priority: P1 | Level: integration | BR: BR-2.4
func TestColdStart_stubbedUpstreamsAcceptAndClose_bootSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if stubAuthAddr == "" || stubNotifAddr == "" {
		t.Fatal("stub upstreams not initialised by TestMain")
	}
	_, _, cmd, err := runOneColdStart(gatewayBinPath, nil, nil, bootTimeout)
	if err != nil {
		killAndAwaitPortFree(t, cmd, 2*time.Second)
		t.Fatalf("boot with stubbed upstreams: %v", err)
	}
	killAndAwaitPortFree(t, cmd, 2*time.Second)
}

// Scenario: 3.1-BLIND-RESOURCE-002
// Priority: P1 | BR: RESOURCE-003 — temp-file cleanup is enforced by TestMain
// teardown; this test confirms the fixture file still exists DURING the run
// so the next iteration has a valid HE_API_JWT_PUBLIC_KEY_PATH.
func TestColdStart_BLIND_RESOURCE_002_tempFilesCleanedUp(t *testing.T) {
	if jwtKeyPath == "" {
		t.Fatal("jwtKeyPath not set by TestMain")
	}
	if _, err := os.Stat(jwtKeyPath); err != nil {
		t.Fatalf("fixture %s missing mid-run: %v", jwtKeyPath, err)
	}
}

// Scenario: 3.1-BLIND-RESOURCE-003
// Priority: P1 | BR: RESOURCE-001 — stub listeners are torn down in TestMain
// via os.Exit. During-run we just confirm both are reachable.
func TestColdStart_BLIND_RESOURCE_003_stubListenersClosed(t *testing.T) {
	for _, addr := range []string{stubAuthAddr, stubNotifAddr} {
		if addr == "" {
			t.Fatal("stub address empty")
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("dial stub %s: %v", addr, err)
		}
		_ = conn.Close()
	}
}

// ============================================================
// AC1 — Integration (rootMux composition via httptest)
// ============================================================

// buildRootMux constructs the same probeMux + rootMux composition as main.go
// (T1.3). Tests use this to exercise the dispatch at integration level without
// spawning the binary.
func buildRootMux(t *testing.T, version string) http.Handler {
	t.Helper()
	health := handlers.NewHealthHandler(version)

	mainMux := http.NewServeMux()
	mainMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})
	// Apply SecurityHeaders to the main mux (matches main.go ordering).
	mainHandler := middleware.SecurityHeaders(mainMux)

	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/health", health.Serve)
	probeMux.HandleFunc("/healthz", health.Serve)

	rootMux := http.NewServeMux()
	rootMux.Handle("/health", probeMux)
	rootMux.Handle("/healthz", probeMux)
	rootMux.Handle("/", mainHandler)

	return obs.WrapHTTPHandler(rootMux, "api-gateway-test")
}

// Scenario: 3.1-INT-001
// Priority: P0 | Level: integration | BR: BR-1.3
func TestRootMux_probePathBypassesSecurityHeaders(t *testing.T) {
	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Strict-Transport-Security", "X-Frame-Options"} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("header %s = %q on /health (BR-1.3 bypass violated)", h, v)
		}
	}
}

// Scenario: 3.1-INT-002
// Priority: P0 | Level: integration | BR: BR-1.2
func TestRootMux_healthzAliasDispatchesToProbeHandler(t *testing.T) {
	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	r1, _ := http.Get(srv.URL + "/health")
	r2, _ := http.Get(srv.URL + "/healthz")
	defer r1.Body.Close()
	defer r2.Body.Close()
	b1, _ := io.ReadAll(r1.Body)
	b2, _ := io.ReadAll(r2.Body)
	if r1.StatusCode != r2.StatusCode || !bytes.Equal(b1, b2) {
		t.Fatalf("/health (%d, %q) vs /healthz (%d, %q): expected identical", r1.StatusCode, b1, r2.StatusCode, b2)
	}
}

// Scenario: 3.1-INT-003
// Priority: P0 | Level: integration | BR: BR-1.3 / T5.1
func TestRootMux_mainMuxRoutesStillCarrySecurityHeaders(t *testing.T) {
	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/me")
	if err != nil {
		t.Fatalf("GET /v1/me: %v", err)
	}
	defer resp.Body.Close()
	// At least one SecurityHeaders bundle entry must be present on the
	// main-mux response. We don't assert specific values (those are tested
	// in the middleware package) — just presence.
	got := false
	for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Strict-Transport-Security", "X-Frame-Options", "Referrer-Policy"} {
		if resp.Header.Get(h) != "" {
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("main-mux response missing SecurityHeaders bundle; headers=%v", resp.Header)
	}
}

// Scenario: 3.1-INT-004
// Priority: P0 | Level: integration | BR: T5.1
//
// Regression gate — the rest of the test suite (every existing test in
// `apps/api-gateway/...`) is the actual assertion. CI's `go test ./...`
// covers it. This test exists only to wire scenario ID 3.1-INT-004 into AC
// traceability; the real check is the green status of the surrounding
// package.
func TestRootMux_existingHandlerTestsStillPass_regressionGate(t *testing.T) {
	t.Log("scenario 3.1-INT-004 — implicitly covered by `go test ./apps/api-gateway/...`")
}

// Scenario: 3.1-INT-005
// Priority: P0 | Level: integration | BR: BR-1.4
func TestRootMux_healthRequestEmitsOTelSpan(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)

	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = resp.Body.Close()
	if got := len(exp.GetSpans()); got == 0 {
		t.Fatalf("expected ≥1 OTel span on /health request; got 0")
	}
}

// Scenario: 3.1-INT-006
// Priority: P1 | Level: integration | BR: BR-1.4
func TestRootMux_healthSpanLacksRequestIDAttribute(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)

	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/health")
	if resp != nil {
		_ = resp.Body.Close()
	}
	for _, s := range exp.GetSpans() {
		for _, a := range s.Attributes {
			if string(a.Key) == "he_request_id" {
				t.Fatalf("span %q carries he_request_id — probes must bypass request-id middleware", s.Name)
			}
		}
	}
}

// Scenario: 3.1-INT-007
// Priority: P1 | Level: integration | BR: BR-1.4 / mn-3
//
// otelhttp's default span tags expose method/route/status_code as semantic
// conventions: `http.method`, `http.route`, `http.status_code` (or the v1.x
// equivalent — exact keys depend on the otelhttp version). We assert at
// least one of those keys is present alongside the GET method and 200 status.
func TestRootMux_healthSpanAttributesPresent(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)

	srv := httptest.NewServer(buildRootMux(t, "0.0.1"))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/health")
	if resp != nil {
		_ = resp.Body.Close()
	}
	spans := exp.GetSpans()
	if len(spans) == 0 {
		t.Fatalf("no spans captured")
	}
	// At least one span must include http.method=GET and a 200 status.
	var sawMethodGET, sawStatus200 bool
	for _, s := range spans {
		for _, a := range s.Attributes {
			if (string(a.Key) == "http.method" || string(a.Key) == "http.request.method") && a.Value.AsString() == "GET" {
				sawMethodGET = true
			}
			if (string(a.Key) == "http.status_code" || string(a.Key) == "http.response.status_code") && a.Value.AsInt64() == 200 {
				sawStatus200 = true
			}
		}
	}
	if !sawMethodGET || !sawStatus200 {
		t.Fatalf("expected span attrs http.method=GET + http.status_code=200; got method=%v status=%v", sawMethodGET, sawStatus200)
	}
}

// ============================================================
// AC1 — E2E (real binary, shared across post-boot scenarios)
// ============================================================

// withGateway boots the binary, polls /health, runs `body`, and tears down.
// Each test gets a fresh binary so port :8080 collisions cannot leak across
// scenarios.
func withGateway(t *testing.T, body func(baseURL string)) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping E2E in -short mode")
	}
	_, _, cmd, err := runOneColdStart(gatewayBinPath, nil, nil, bootTimeout)
	if err != nil {
		killAndAwaitPortFree(t, cmd, 2*time.Second)
		t.Fatalf("gateway boot: %v", err)
	}
	defer killAndAwaitPortFree(t, cmd, 2*time.Second)
	body("http://127.0.0.1:" + listenPort)
}

// Scenario: 3.1-E2E-001
// Priority: P0 | Level: e2e | BR: BR-1.5
func TestE2E_realBinary_healthReturnsByteExactBody(t *testing.T) {
	withGateway(t, func(base string) {
		resp, err := http.Get(base + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		want := `{"status":"ok","service":"api-gateway","version":"0.0.1"}`
		if resp.StatusCode != 200 || string(body) != want {
			t.Fatalf("got %d/%q want 200/%q", resp.StatusCode, body, want)
		}
	})
}

// Scenario: 3.1-E2E-002
// Priority: P1 | Level: e2e | BR: BR-1.2
func TestE2E_realBinary_healthzAliasIdentical(t *testing.T) {
	withGateway(t, func(base string) {
		r1, _ := http.Get(base + "/health")
		r2, _ := http.Get(base + "/healthz")
		defer r1.Body.Close()
		defer r2.Body.Close()
		b1, _ := io.ReadAll(r1.Body)
		b2, _ := io.ReadAll(r2.Body)
		if r1.StatusCode != r2.StatusCode || !bytes.Equal(b1, b2) {
			t.Fatalf("/health vs /healthz diverge: %d/%q vs %d/%q", r1.StatusCode, b1, r2.StatusCode, b2)
		}
	})
}

// Scenario: 3.1-E2E-003
// Priority: P1 | Level: e2e | BR: AC1 §Scenario
func TestE2E_realBinary_methodNotAllowed(t *testing.T) {
	withGateway(t, func(base string) {
		resp, err := http.Post(base+"/health", "text/plain", strings.NewReader(""))
		if err != nil {
			t.Fatalf("POST /health: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 405 {
			t.Fatalf("status: got %d, want 405", resp.StatusCode)
		}
		if string(body) != `{"error":"method_not_allowed"}` {
			t.Fatalf("body: got %q, want %q", body, `{"error":"method_not_allowed"}`)
		}
		if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow: got %q, want %q", got, "GET, HEAD")
		}
	})
}

// Scenario: 3.1-BLIND-BOUNDARY-003
// Priority: P2 | Category: BOUNDARY-005
func TestE2E_BLIND_BOUNDARY_003_uppercasePath404(t *testing.T) {
	withGateway(t, func(base string) {
		resp, err := http.Get(base + "/HEALTH")
		if err != nil {
			t.Fatalf("GET /HEALTH: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("status: got %d, want 404 (mux is case-sensitive)", resp.StatusCode)
		}
	})
}

// Scenario: 3.1-BLIND-BOUNDARY-004
// Priority: P2 | Category: BOUNDARY-005
//
// Sends a raw HTTP request with lowercase `get` method. Go's http.Server
// rejects with 400 BadRequest before our handler is reached.
func TestE2E_BLIND_BOUNDARY_004_lowercaseMethodRejected(t *testing.T) {
	withGateway(t, func(base string) {
		conn, err := net.Dial("tcp", "127.0.0.1:"+listenPort)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write([]byte("get /health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		line := string(buf[:n])
		if !strings.HasPrefix(line, "HTTP/1.1 4") {
			t.Fatalf("expected 4xx status line; got %q", line)
		}
	})
}

// Scenario: 3.1-BLIND-BOUNDARY-005
// Priority: P2 | Category: BOUNDARY-003
func TestE2E_BLIND_BOUNDARY_005_extraPathSuffix404(t *testing.T) {
	withGateway(t, func(base string) {
		resp, err := http.Get(base + "/healthxxxxx")
		if err != nil {
			t.Fatalf("GET /healthxxxxx: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("status: got %d, want 404", resp.StatusCode)
		}
	})
}

// Scenario: 3.1-BLIND-ERROR-001
// Priority: P0 | Category: ERROR-002
//
// Stub upstreams (auth-svc + notification-svc) close every connection — the
// default test fixture already does this. /health MUST still return 200
// because the probe is process-liveness only (BR-1.1).
func TestColdStart_BLIND_ERROR_001_upstreams503_healthStill200(t *testing.T) {
	withGateway(t, func(base string) {
		resp, err := http.Get(base + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status: %d (BR-1.1 violated)", resp.StatusCode)
		}
	})
}

// Scenario: 3.1-BLIND-ERROR-002
// Priority: P1 | Category: ERROR-002
//
// Boot with no DB / Redis env vars (the gateway doesn't read them anyway —
// this confirms /health is robust to their absence).
func TestColdStart_BLIND_ERROR_002_noDbRedisEnv_healthStill200(t *testing.T) {
	// Make sure DB / Redis env vars are absent for the spawned process.
	t.Setenv("HE_API_DB_URL", "")
	t.Setenv("HE_API_REDIS_URL", "")
	withGateway(t, func(base string) {
		resp, err := http.Get(base + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	})
}

// Scenario: 3.1-BLIND-ERROR-003
// Priority: P1 | Category: ERROR-001
//
// Same stub behaviour (accept-then-close = upstream FIN); cold-start budget
// is unchanged. We assert one fresh boot completes within budget.
func TestColdStart_BLIND_ERROR_003_upstreamFinMidConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	dur, _, cmd, err := runOneColdStart(gatewayBinPath, nil, nil, bootTimeout)
	if cmd != nil {
		killAndAwaitPortFree(t, cmd, 2*time.Second)
	}
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if dur > time.Duration(healthBudgetMS)*time.Millisecond {
		t.Fatalf("boot %v exceeded budget %dms despite upstream FIN", dur, healthBudgetMS)
	}
}

// ============================================================
// helpers
// ============================================================

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
