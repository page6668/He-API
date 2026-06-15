package heapi

import (
	"os"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// DEFAULT_BASE_URL is the production He-API gateway endpoint used when neither
// an explicit option.WithBaseURL(...) nor the HE_API_BASE_URL environment
// variable is set. It mirrors the Python (10.2) and TypeScript (10.3) SDK
// defaults (R-OQ-10.4-3). If the gateway domain changes, this single constant
// is the only edit required.
const DEFAULT_BASE_URL = "https://api.he-api.com/v1"

// Environment variables read by NewClient. The SDK deliberately reads HE_API_*
// and NEVER OpenAI's OPENAI_API_KEY / OPENAI_BASE_URL, so a process holding a
// real OpenAI credential cannot accidentally route He-API traffic with it
// (UNIT-012). openai-go's DefaultClientOptions() would otherwise pick those up;
// NewClient always passes an overriding option to neutralize them.
const (
	envAPIKey  = "HE_API_KEY"
	envBaseURL = "HE_API_BASE_URL"
)

// NewClient returns an upstream openai.Client pre-configured for the He-API
// gateway. The returned value IS the openai-go client (no custom wrapper type),
// so it is a literal drop-in for openai.NewClient — every service field
// (Chat, Embeddings, Models, Audio, ...) and every type/error is openai-go's.
//
// Resolution precedence (last option wins, matching openai-go semantics):
//
//	base URL: explicit option.WithBaseURL > HE_API_BASE_URL env > DEFAULT_BASE_URL
//	API key:  explicit option.WithAPIKey  > HE_API_KEY env       > (none)
//
// The default base URL and HE_API_KEY are appended BEFORE the caller's opts so
// any explicit option overrides them; they are always present so openai-go's
// OPENAI_BASE_URL / OPENAI_API_KEY environment defaults can never take effect.
func NewClient(opts ...option.RequestOption) openai.Client {
	defaults := []option.RequestOption{
		option.WithBaseURL(resolveBaseURL()),
		// Always set: an empty value (HE_API_KEY unset) overrides any
		// OPENAI_API_KEY from openai-go's env defaults to empty, falling
		// through to openai-go's native key-missing behavior (BOUNDARY-002).
		option.WithAPIKey(os.Getenv(envAPIKey)),
	}
	return openai.NewClient(append(defaults, opts...)...)
}

// resolveBaseURL implements the env tier of the base-URL precedence: an unset OR
// empty HE_API_BASE_URL falls back to DEFAULT_BASE_URL (never an empty host —
// BOUNDARY-003).
func resolveBaseURL() string {
	if v := os.Getenv(envBaseURL); v != "" {
		return v
	}
	return DEFAULT_BASE_URL
}
