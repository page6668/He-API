package templates

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	htmltmpl "html/template"
	"strings"
	"sync"
	texttmpl "text/template"
)

//go:embed email_verification/*.txt email_verification/*.html
//go:embed gdpr_export_ready/*.txt gdpr_export_ready/*.html
//go:embed cap_warning/*.txt cap_warning/*.html
//go:embed cap_tripped/*.txt cap_tripped/*.html
var fs embed.FS

// FallbackLocale is the locale that any non-resolvable locale falls back to.
// Matches Story 2.1 i18n default + matches the SendEmailRequest.locale proto
// comment ("falls back to 'en'").
const FallbackLocale = "en"

// Template names. Keep in lockstep with the EmailTemplate enum in
// packages/proto/he/notification/v1/notification.proto.
const (
	TemplateEmailVerification = "email_verification"
	// Story 2.6 — slug for the GDPR data-export "your zip is ready" email
	// (Architect Round 1 Ruling R-2 — the slug is internal to this
	// package; the proto wire contract uses the EmailTemplate enum).
	TemplateGDPRExportReady = "gdpr_export_ready"
	// Story 5.4 — monthly-cost-cap threshold emails. Slugs internal to this
	// package; the proto wire contract uses the EmailTemplate enum (R-2 cascade).
	TemplateMonthlyCapWarning = "cap_warning"
	TemplateMonthlyCapTripped = "cap_tripped"
)

// Rendered is what the SendGrid client needs to build one outbound mail.
type Rendered struct {
	Subject  string // first non-comment, non-blank line of the .txt template
	TextBody string // remainder of the .txt template
	HTMLBody string // full .html template (HTML comments at the top pass through harmlessly)
}

// ErrTemplateNotFound is returned when the requested template name is unknown.
var ErrTemplateNotFound = errors.New("templates: template not found")

// ErrMissingVariable is returned when the caller's vars map omits a key the
// template references via Go template-action syntax.
var ErrMissingVariable = errors.New("templates: missing required variable")

// Renderer compiles templates lazily from the embedded FS and caches them
// per (name, locale) pair. Safe for concurrent use.
type Renderer struct {
	mu    sync.RWMutex
	cache map[string]*compiled
}

type compiled struct {
	subject  *texttmpl.Template
	textBody *texttmpl.Template
	htmlBody *htmltmpl.Template
}

// NewRenderer returns a ready-to-use Renderer. The embedded template FS is
// global to the package; if templates need to be reloaded at runtime, a new
// Renderer can be constructed (e.g., for tests).
func NewRenderer() *Renderer {
	return &Renderer{cache: make(map[string]*compiled)}
}

// Render returns the per-locale rendered subject + text body + HTML body for
// the named template, substituting vars into all three. If locale is unknown,
// falls back to FallbackLocale. The "Missingkey=error" option is set so any
// referenced-but-absent variable surfaces immediately as ErrMissingVariable.
func (r *Renderer) Render(name, locale string, vars map[string]string) (Rendered, error) {
	c, err := r.compile(name, locale)
	if err != nil {
		return Rendered{}, err
	}
	subj, err := executeText(c.subject, vars)
	if err != nil {
		return Rendered{}, err
	}
	text, err := executeText(c.textBody, vars)
	if err != nil {
		return Rendered{}, err
	}
	html, err := executeHTML(c.htmlBody, vars)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{Subject: subj, TextBody: text, HTMLBody: html}, nil
}

func (r *Renderer) compile(name, locale string) (*compiled, error) {
	resolved := r.resolveLocale(name, locale)
	key := name + "/" + resolved
	r.mu.RLock()
	if c, ok := r.cache[key]; ok {
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	// Re-check under write lock to avoid double-compile races.
	if c, ok := r.cache[key]; ok {
		return c, nil
	}

	txtPath := name + "/" + resolved + ".txt"
	htmlPath := name + "/" + resolved + ".html"

	txtRaw, err := fs.ReadFile(txtPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, txtPath)
	}
	htmlRaw, err := fs.ReadFile(htmlPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, htmlPath)
	}

	subject, textBody := splitSubjectAndBody(string(txtRaw))
	subjectT, err := texttmpl.New("subject").Option("missingkey=error").Parse(subject)
	if err != nil {
		return nil, fmt.Errorf("templates: parse subject %s/%s: %w", name, resolved, err)
	}
	textBodyT, err := texttmpl.New("text").Option("missingkey=error").Parse(textBody)
	if err != nil {
		return nil, fmt.Errorf("templates: parse text body %s/%s: %w", name, resolved, err)
	}
	htmlBodyT, err := htmltmpl.New("html").Option("missingkey=error").Parse(string(htmlRaw))
	if err != nil {
		return nil, fmt.Errorf("templates: parse html body %s/%s: %w", name, resolved, err)
	}

	c := &compiled{subject: subjectT, textBody: textBodyT, htmlBody: htmlBodyT}
	r.cache[key] = c
	return c, nil
}

// resolveLocale picks the most specific available locale for (name, locale),
// falling back to FallbackLocale. The check is cheap because templates are
// embedded — fs.ReadFile is in-memory.
func (r *Renderer) resolveLocale(name, locale string) string {
	if locale != "" {
		if _, err := fs.ReadFile(name + "/" + locale + ".txt"); err == nil {
			if _, err := fs.ReadFile(name + "/" + locale + ".html"); err == nil {
				return locale
			}
		}
	}
	return FallbackLocale
}

// splitSubjectAndBody parses the .txt content into (subject, body).
//
// Leading lines whose first non-whitespace char is `#` are treated as comments
// and skipped — this preserves the per-locale `# TODO: localize` markers in
// the placeholder locales without leaking them into the rendered email.
//
// The first non-comment, non-blank line becomes the subject. Any blank lines
// immediately after it are skipped. The remainder is the text body, trimmed
// of trailing whitespace.
func splitSubjectAndBody(raw string) (string, string) {
	lines := strings.Split(raw, "\n")
	var i int
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		break
	}
	if i >= len(lines) {
		return "", ""
	}
	subject := strings.TrimSpace(lines[i])
	i++
	// Skip blank separator lines.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	body := strings.Join(lines[i:], "\n")
	body = strings.TrimRight(body, "\n\r\t ")
	return subject, body
}

func executeText(t *texttmpl.Template, vars map[string]string) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		if isMissingKey(err) {
			return "", ErrMissingVariable
		}
		return "", err
	}
	return buf.String(), nil
}

func executeHTML(t *htmltmpl.Template, vars map[string]string) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		if isMissingKey(err) {
			return "", ErrMissingVariable
		}
		return "", err
	}
	return buf.String(), nil
}

func isMissingKey(err error) bool {
	if err == nil {
		return false
	}
	// text/template + html/template both return errors whose String contains
	// "map has no entry for key" when Missingkey=error is set.
	return strings.Contains(err.Error(), "map has no entry for key")
}
