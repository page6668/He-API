package heapi

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// openai-go pin band (BR-10.4.9 / R-OQ-10.4-1 子项②). The dependency is GA at
// major v3 (import path .../openai-go/v3). The SDK is validated against this
// major and a minor floor; a build that resolves openai-go OUTSIDE the band
// (a v4 major bump, or a downgrade below the validated floor) is a drop-in
// hazard and the pin-drift guard test (UNIT-014) fails loud — mirroring the
// Python test_sdk_pin_guard_openai and TS 10.3-UNIT-003 guards. The band is a
// floor, not an exact pin, so patch/minor upgrades within v3 stay frictionless
// and a published consumer's dependency dedup is never broken.
const (
	openAIGoModulePath = "github.com/openai/openai-go/v3"
	openAIGoMajor      = 3
	openAIGoMinMinor   = 39 // validated floor: v3.39.0 (the go.mod require)
)

// installedOpenAIGoVersion returns the openai-go/v3 version actually linked into
// the running binary, read from the module build info. ok is false only when
// build info is unavailable (e.g. a binary built without module info).
func installedOpenAIGoVersion() (version string, ok bool) {
	info, available := debug.ReadBuildInfo()
	if !available {
		return "", false
	}
	for _, dep := range info.Deps {
		if dep.Path == openAIGoModulePath {
			v := dep.Version
			if dep.Replace != nil && dep.Replace.Version != "" {
				v = dep.Replace.Version
			}
			return v, v != ""
		}
	}
	return "", false
}

// parseMajorMinor parses a semantic version like "v3.39.0" into its major and
// minor components. ok is false for pseudo/unparseable versions.
func parseMajorMinor(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// openAIGoVersionInBand reports whether v is within the validated pin band
// [v{major}.{minMinor}.0, v{major+1}.0.0).
func openAIGoVersionInBand(v string) bool {
	major, minor, ok := parseMajorMinor(v)
	if !ok {
		return false
	}
	return major == openAIGoMajor && minor >= openAIGoMinMinor
}
