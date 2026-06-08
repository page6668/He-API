package routingclient

import (
	"errors"
	"net/http"
	"strings"
)

// ABModelsHeader is the Story-6.4 request header carrying the A/B comparison
// leg ids as a comma-separated list (e.g. "qwen-max,deepseek-v3"). Its presence
// makes the request a dual-leg A/B dispatch and OVERRIDES strategy selection
// (Q-I). Documented-but-unwired before 6.4 (rest-api-spec §5.1.1).
const ABModelsHeader = "X-He-AB-Models"

// ErrInvalidABModels is the parse failure for an X-He-AB-Models header that does
// not resolve to EXACTLY 2 distinct ids after trim+dedup (Q-H/BR1-4). The
// handler maps it to a 400_invalid_request envelope.
var ErrInvalidABModels = errors.New("X-He-AB-Models requires exactly 2 distinct models")

// ABModelsPresent reports whether the request carries a non-blank X-He-AB-Models
// header. It keys the streaming guard (BR4-2): stream=true + a present A/B
// header is rejected BEFORE any parse/dispatch, regardless of whether the value
// is a valid 2-id list. A whitespace-only value is treated as absent.
func ABModelsPresent(header http.Header) bool {
	return strings.TrimSpace(header.Get(ABModelsHeader)) != ""
}

// ParseABModels parses the X-He-AB-Models header into the A/B comparison legs
// (Q-H/BR1-4). It splits on ",", trims each id, drops empty segments, and
// de-duplicates BEFORE the count check (so "a,a" collapses to 1 -> error). The
// result MUST be EXACTLY 2 distinct ids or it returns ErrInvalidABModels.
//
// A blank/absent header returns (nil, nil) — NOT an A/B request — so the
// non-A/B routing path proceeds byte-for-byte unchanged (zero regression).
// Concrete-vs-meta validation is NOT done here: routing-svc owns the
// concrete-catalogue gate (BR1-2), so a he-router-*/unknown leg surfaces as a
// 400 from the routing decision, not from this syntactic parse.
func ParseABModels(header http.Header) ([]string, error) {
	raw := strings.TrimSpace(header.Get(ABModelsHeader))
	if raw == "" {
		return nil, nil // not an A/B request
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			continue // tolerate stray/trailing commas
		}
		if _, dup := seen[id]; dup {
			continue // dedup BEFORE count (BR1-4: a,a -> 1 -> error)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) != 2 {
		return nil, ErrInvalidABModels
	}
	return out, nil
}
