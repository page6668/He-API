// Package providerrefresher implements AD-004 runtime provider configuration:
// each LLM adapter polls the api-gateway's loopback-only
// GET /internal/providers/active endpoint and hot-swaps its upstream
// api_key / base_url without a process restart.
//
// The endpoint is restricted to loopback (127.0.0.0/8, ::1) at the gateway, so
// the adapter must call it over localhost. Credentials never hit the wire
// off-machine, and the gateway never returns them to non-loopback callers.
package providerrefresher

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Target is the interface an adapter's upstream client implements so the
// refresher can push hot-updated credentials into it.
type Target interface {
	SetKey(key string)
	SetBaseURL(url string)
}

// Provider is one entry in the gateway's active-provider list.
// Field names match apps/api-gateway/internal/providers.Store.ProviderActive.
type Provider struct {
	Name    string `json:"provider_name"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
}

type activeResponse struct {
	Data []Provider `json:"data"`
}

// Refresher polls the gateway and pushes matching credentials to Target.
type Refresher struct {
	gatewayURL   string
	providerName string
	target       Target
	logger       Logger
	interval     time.Duration
	httpClient   *http.Client

	mu      sync.RWMutex
	current Provider
}

// Logger is a minimal slog-compatible logging surface.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// New constructs a Refresher. gatewayURL is the loopback base URL of the
// api-gateway (e.g. http://127.0.0.1:8080); providerName is this adapter's
// provider key (e.g. "deepseek").
func New(gatewayURL, providerName string, target Target, logger Logger) *Refresher {
	if logger == nil {
		logger = nopLogger{}
	}
	return &Refresher{
		gatewayURL:   strings.TrimRight(gatewayURL, "/"),
		providerName: providerName,
		target:       target,
		logger:       logger,
		interval:     30 * time.Second,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// WithInterval overrides the poll interval (for tests / operators).
func (r *Refresher) WithInterval(d time.Duration) *Refresher {
	if d > 0 {
		r.interval = d
	}
	return r
}

// Start blocks, polling every interval until ctx is cancelled. Call in a
// goroutine from main.
func (r *Refresher) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.poll(ctx)
		}
	}
}

func (r *Refresher) poll(ctx context.Context) {
	url := r.gatewayURL + "/internal/providers/active"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.logger.Error("provider refresher: build request", "error", err.Error())
		return
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.logger.Warn("provider refresher: fetch failed", "url", url, "error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.logger.Warn("provider refresher: non-200", "status", resp.StatusCode)
		return
	}
	var body activeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		r.logger.Warn("provider refresher: decode", "error", err.Error())
		return
	}
	for _, p := range body.Data {
		if p.Name == r.providerName {
			r.apply(p)
			return
		}
	}
	r.logger.Warn("provider refresher: provider not in active list", "name", r.providerName)
}

func (r *Refresher) apply(p Provider) {
	// Skip disabled providers — gateway signals them explicitly.
	if !p.Enabled {
		r.logger.Warn("provider refresher: provider disabled in gateway — keeping last config", "name", p.Name)
		return
	}
	// Never overwrite with an empty key — a partial/garbled gateway response
	// must not wipe live credentials.
	if p.APIKey == "" {
		r.logger.Warn("provider refresher: gateway returned empty key — skipped", "name", p.Name)
		return
	}
	r.mu.RLock()
	unchanged := r.current.APIKey == p.APIKey && r.current.BaseURL == p.BaseURL
	r.mu.RUnlock()
	if unchanged {
		return
	}
	r.target.SetKey(p.APIKey)
	r.target.SetBaseURL(p.BaseURL)
	r.mu.Lock()
	r.current = p
	r.mu.Unlock()
	r.logger.Info("provider refresher: credentials updated", "name", p.Name, "base_url", p.BaseURL)
}

// nopLogger discards everything (used when caller passes nil).
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}
