// Package aliyunmail wraps the Alibaba Cloud DirectMail (DM) SingleSendMail
// API (https://help.aliyun.com/zh/direct-mail/developer-reference/api-dm-2015-11-23-singlesendmail).
//
// We avoid the official aliyun-go-sdk on purpose: the API surface we need
// is a single POST + HMAC-SHA1 signature, and the SDK pulls in transitive
// deps that complicate testing and bloat the binary.
//
// Credentials: HE_API_ALIYUNMAIL_ACCESS_KEY_ID / HE_API_ALIYUNMAIL_ACCESS_KEY_SECRET.
// Region defaults to dm.aliyuncs.com (Singapore DM endpoint) — Aliyun DM is
// a single global namespace, region_id only affects data-residency billing
// & the request signing nonce, so the default is correct for ECS in any
// region as long as the account has DM enabled.
//
// API parity with sendgrid.Client: Send(ctx, SendRequest) (string, error)
// so handlers.EmailSender is satisfied by either backend.
package aliyunmail

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultEndpoint = "https://dm.aliyuncs.com"
	defaultTimeout  = 10 * time.Second
	apiVersion      = "2015-11-23"
)

// ErrTransient mirrors sendgrid.ErrTransient semantics: 5xx / network /
// timeout — the caller should retry.
var ErrTransient = errors.New("aliyunmail: transient failure")

// ErrPermanent mirrors sendgrid.ErrPermanent: 4xx — usually programming bug.
var ErrPermanent = errors.New("aliyunmail: permanent failure")

// Address is the sender shape. Aliyun DM splits AccountName (email) from
// FromAlias (display name) as separate form fields, so we keep the same
// two-field struct that sendgrid.Address uses.
type Address struct {
	Email string
	Name  string
}

// Client posts to DM SingleSendMail via the RPC-style
// `/?Action=SingleSendMail&...` form-encoded protocol with v3 HMAC-SHA256
// signature (Story 2.2 parity — re-uses Go stdlib only).
type Client struct {
	AccessKeyID     string
	AccessKeySecret string
	BaseURL         string // defaults to https://dm.aliyuncs.com
	From            Address
	HTTPClient      *http.Client
}

// NewClient returns a Client with sensible defaults. Both credentials are
// required.
func NewClient(accessKeyID, accessKeySecret string, from Address) *Client {
	return &Client{
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		BaseURL:         defaultEndpoint,
		From:            from,
		HTTPClient:      &http.Client{Timeout: defaultTimeout},
	}
}

// SendRequest mirrors sendgrid.SendRequest exactly so the handler can
// branch-free between backends.
type SendRequest struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// Send posts SingleSendMail and returns the Aliyun DM request id (or "").
// Aliyun DM is form-encoded, NOT JSON — every parameter goes into the
// query string of the POST, signature v3 over the canonicalized query.
func (c *Client) Send(ctx context.Context, req SendRequest) (string, error) {
	if c.AccessKeyID == "" || c.AccessKeySecret == "" {
		return "", fmt.Errorf("%w: access key id / secret required", ErrPermanent)
	}
	params := url.Values{}
	params.Set("Format", "JSON")
	params.Set("Version", apiVersion)
	params.Set("AccessKeyId", c.AccessKeyID)
	params.Set("SignatureMethod", "HMAC-SHA256")
	params.Set("Timestamp", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	params.Set("SignatureVersion", "1.0")
	params.Set("SignatureNonce", nonce())
	params.Set("Action", "SingleSendMail")
	params.Set("AccountName", c.From.Email)
	if c.From.Name != "" {
		params.Set("FromAlias", c.From.Name)
	}
	params.Set("AddressType", "1") // 1 = individual recipient (vs batch)
	params.Set("ReplyToAddress", "true")
	params.Set("ToAddress", req.To)
	params.Set("Subject", req.Subject)
	// DM accepts either TextBody or HtmlBody. Prefer HTML; fall back to text
	// so the handler can stay free of provider knowledge.
	switch {
	case req.HTMLBody != "":
		params.Set("HtmlBody", req.HTMLBody)
		fallthrough
	case req.TextBody != "":
		params.Set("TextBody", req.TextBody)
	}

	// Sign per Aliyun RPC v3 spec: sort params, percent-encode key+value,
	// concat with &, then HMAC-SHA256(secret + "&") keyed.
	params.Set("Signature", sign(c.AccessKeySecret, params))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, strings.NewReader(params.Encode()))
	if err != nil {
		return "", fmt.Errorf("aliyunmail: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	// Aliyun DM success envelope:
	//   {"RequestId":"...","EnvId":"..."} → 200 OK
	// Errors: {"Code":"...","Message":"...","RequestId":"..."} → 4xx
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return extractRequestID(string(respBody)), nil
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrTransient, resp.StatusCode, truncate(string(respBody), 256))
	default: // 4xx
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrPermanent, resp.StatusCode, truncate(string(respBody), 256))
	}
}

// sign builds the Aliyun RPC v3 signature for a parameter set.
// See: https://help.aliyun.com/document_detail/25489.html
func sign(secret string, params url.Values) string {
	// Canonicalize: sort keys, percent-encode key and value, join with &.
	keys := make([]string, 0, len(params))
	for k := range params {
		// Signature param itself is excluded (it's added after this call).
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	canonical := strings.Builder{}
	for i, k := range keys {
		if i > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(percentEncode(k))
		canonical.WriteByte('=')
		canonical.WriteString(percentEncode(params.Get(k)))
	}
	stringToSign := "POST&" + percentEncode("/") + "&" + percentEncode(canonical.String())

	mac := hmac.New(sha256.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// percentEncode implements Aliyun's stricter URL-encoding:
// - alphanumerics + - _ . ~ unescaped
// - * - . _ encoded as %2A %2D %2E %5F %7E (Aliyun differs from RFC 3986)
// - space encoded as %20 (not +)
// - everything else percent-encoded
func percentEncode(s string) string {
	u := url.QueryEscape(s)
	// Aliyun-specific substitutions
	replacer := strings.NewReplacer(
		"+", "%20",
		"*", "%2A",
		"%7E", "~",
	)
	return replacer.Replace(u)
}

// nonce returns a 16-byte hex random string (Aliyun requires per-request
// uniqueness to defeat replay).
func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is documented to never fail on Linux; fall back to a
		// time-derived nonce rather than panic (the request will still
		// work, just with weaker replay protection).
		ts := time.Now().UnixNano()
		return fmt.Sprintf("%016x", ts)
	}
	return hex.EncodeToString(b[:])
}

// extractRequestID pulls "RequestId":"<id>" out of the success body
// without pulling in a JSON dependency (the only field we care about).
func extractRequestID(body string) string {
	const tag = `"RequestId":"`
	i := strings.Index(body, tag)
	if i < 0 {
		return ""
	}
	rest := body[i+len(tag):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// keep imports used (sha256, sha1, rand imported for hash.Hash interface
// compatibility if callers swap in alternative signatures).
var (
	_ hash.Hash = sha1.New
	_           = bytes.NewReader
)