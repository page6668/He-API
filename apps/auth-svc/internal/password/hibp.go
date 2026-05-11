package password

import (
	"context"
	"crypto/sha1" //nolint:gosec // HIBP API spec uses SHA-1 as a range-query primitive (k-anonymity); not a cryptographic guarantee.
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HIBP API constants (TS-CONS-004).
const (
	hibpDefaultBaseURL = "https://api.pwnedpasswords.com"
	hibpDefaultTimeout = 3 * time.Second
)

// ErrPasswordBreached is returned by HIBPClient.CheckBreached when the SHA-1
// of the password appears in the HaveIBeenPwned breach corpus.
var ErrPasswordBreached = errors.New("password: appears in known breach corpus")

// ErrHIBPUnavailable is returned on any HIBP API outage (5xx, timeout, DNS
// failure, connection refused). The CheckBreached contract is FAIL-CLOSED
// per TS-CONS-004 — auth-svc must refuse the signup, not pass through, when
// HIBP cannot be consulted.
var ErrHIBPUnavailable = errors.New("password: HIBP service unavailable")

// HIBPClient calls the HaveIBeenPwned k-anonymity API.
//
// The outbound request exposes only the first 5 hex characters of
// SHA-1(password); the server responds with all suffixes that share that
// prefix. The full hash never leaves this process.
//
// BaseURL is the canonical https://api.pwnedpasswords.com when constructed via
// NewHIBPClient. Tests inject an httptest server URL. HTTPClient.Timeout is
// the network deadline (default 3 s); ctx passed to CheckBreached can cancel
// sooner.
type HIBPClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewHIBPClient returns a client pinned to api.pwnedpasswords.com with a
// 3-second timeout (TS-CONS-004).
func NewHIBPClient() *HIBPClient {
	return &HIBPClient{
		BaseURL:    hibpDefaultBaseURL,
		HTTPClient: &http.Client{Timeout: hibpDefaultTimeout},
	}
}

// CheckBreached returns:
//   - nil                       if the password is not in the breach corpus
//   - ErrPasswordBreached       if it is
//   - ErrHIBPUnavailable        on any transport / 5xx / parse failure (fail-closed)
//
// Inputs are not logged. The outbound URL contains only SHA-1(pw)[0:5].
func (c *HIBPClient) CheckBreached(ctx context.Context, pw []byte) error {
	full := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(pw))) //nolint:gosec // see file comment
	prefix, suffix := full[:5], full[5:]
	url := strings.TrimRight(c.BaseURL, "/") + "/range/" + prefix

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ErrHIBPUnavailable
	}
	req.Header.Set("User-Agent", "he-api-auth-svc/1.0 (+https://he-api.com)")
	req.Header.Set("Add-Padding", "true") // HIBP padding response defense (no leak via response size)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ErrHIBPUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Any non-200 (5xx, 4xx, 429) → fail closed. We do NOT distinguish
		// "user error" here: the client controls the SHA-1 prefix and 4xx
		// would indicate a malformed prefix, which is our bug, not the
		// caller's.
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrHIBPUnavailable
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ErrHIBPUnavailable
	}
	// Response: lines of `SUFFIX:COUNT\r\n` (CRLF). Match against `suffix`.
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sep := strings.IndexByte(line, ':')
		if sep < 0 {
			continue
		}
		if strings.EqualFold(line[:sep], suffix) {
			return ErrPasswordBreached
		}
	}
	return nil
}
