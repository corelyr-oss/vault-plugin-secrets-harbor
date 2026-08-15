package backend

import (
	"regexp"
	"strings"

	semver "github.com/hashicorp/go-version"
)

var nonSemverChars = regexp.MustCompile(`[^0-9A-Za-z.-]+`)

// normalizeVersion returns v if it is a valid semantic version (Vault refuses
// to enable plugins that self-report anything else), an empty string if v is
// empty, and otherwise a valid pre-release of v0.0.0 carrying v as metadata,
// e.g. "dev" -> "v0.0.0-dev", "abc1234-dirty" -> "v0.0.0-abc1234-dirty".
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if _, err := semver.NewSemver(v); err == nil {
		return v
	}
	s := strings.Trim(nonSemverChars.ReplaceAllString(v, "-"), "-.")
	if s == "" {
		s = "dev"
	}
	return "v0.0.0-" + s
}
