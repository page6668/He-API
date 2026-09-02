package providerrefresher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeTarget struct {
	key string
	url string
}

func (f *fakeTarget) SetKey(k string)     { f.key = k }
func (f *fakeTarget) SetBaseURL(u string) { f.url = u }

type testLogger struct{ lastWarn string }

func (l *testLogger) Info(string, ...any)  {}
func (l *testLogger) Warn(m string, a ...any) { l.lastWarn = m }
func (l *testLogger) Error(string, ...any) {}

func TestPollUpdatesCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"provider_name":"deepseek","api_key":"sk-new","base_url":"https://api.v2","enabled":true}]}`))
	}))
	defer srv.Close()

	target := &fakeTarget{}
	r := New(srv.URL, "deepseek", target, &testLogger{})
	r.poll(context.Background())

	if target.key != "sk-new" {
		t.Fatalf("expected key sk-new, got %q", target.key)
	}
	if target.url != "https://api.v2" {
		t.Fatalf("expected url https://api.v2, got %q", target.url)
	}
}

func TestPollSkipsWrongProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"provider_name":"doubao","api_key":"x","base_url":"y","enabled":true}]}`))
	}))
	defer srv.Close()

	logger := &testLogger{}
	target := &fakeTarget{key: "keep", url: "keep-url"}
	r := New(srv.URL, "deepseek", target, logger)
	r.poll(context.Background())

	if target.key != "keep" {
		t.Fatalf("expected key unchanged, got %q", target.key)
	}
	if logger.lastWarn == "" {
		t.Fatalf("expected a warn log for missing provider")
	}
}

func TestApplySkipsEmptyKey(t *testing.T) {
	logger := &testLogger{}
	target := &fakeTarget{key: "live", url: "live-url"}
	r := New("http://x", "deepseek", target, logger)
	// empty key must NOT overwrite live creds
	r.apply(Provider{Name: "deepseek", APIKey: "", BaseURL: "https://z", Enabled: true})
	if target.key != "live" {
		t.Fatalf("empty key must not wipe live credentials, got %q", target.key)
	}
}

func TestApplySkipsDisabled(t *testing.T) {
	logger := &testLogger{}
	target := &fakeTarget{key: "live", url: "live-url"}
	r := New("http://x", "deepseek", target, logger)
	r.apply(Provider{Name: "deepseek", APIKey: "new", BaseURL: "https://z", Enabled: false})
	if target.key != "live" {
		t.Fatalf("disabled provider must not overwrite, got %q", target.key)
	}
}

func TestApplyIdempotent(t *testing.T) {
	logger := &testLogger{}
	target := &fakeTarget{}
	r := New("http://x", "deepseek", target, logger)
	r.apply(Provider{Name: "deepseek", APIKey: "k", BaseURL: "u", Enabled: true})
	// second identical apply must not log "credentials updated"
	r.logger = logger
	r.apply(Provider{Name: "deepseek", APIKey: "k", BaseURL: "u", Enabled: true})
	if logger.lastWarn != "" {
		t.Fatalf("expected no warn on unchanged config, got %q", logger.lastWarn)
	}
}
