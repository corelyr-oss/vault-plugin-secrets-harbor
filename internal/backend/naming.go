package backend

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	defaultRobotNamePrefix = "vault"
	// maxRoleSegment bounds the normalized role segment so the full name stays
	// far below Harbor's 255 character column even with a long prefix.
	maxRoleSegment = 64
	suffixHexLen   = 8
)

// harborRobotNameRe is Harbor's validation for the short robot name.
var harborRobotNameRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeSegment lowercases s and collapses any run of characters that are
// not [a-z0-9] into a single '-', trimming separators from both ends. It
// returns "" if nothing survives.
func normalizeSegment(s string) string {
	s = strings.ToLower(s)
	s = nonAlnumRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxRoleSegment {
		s = strings.Trim(s[:maxRoleSegment], "-")
	}
	return s
}

// robotName builds a Harbor-valid short robot name from the configured prefix,
// the role name and a random suffix, e.g. "vault-ci-builder-3f9a1c2b".
func robotName(prefix, role string) (string, error) {
	suffix, err := randomHex(suffixHexLen)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 3)
	if p := normalizeSegment(prefix); p != "" {
		parts = append(parts, p)
	}
	if r := normalizeSegment(role); r != "" {
		parts = append(parts, r)
	}
	parts = append(parts, suffix)
	name := strings.Join(parts, "-")
	if !harborRobotNameRe.MatchString(name) {
		// Should be unreachable given normalization; guard anyway.
		name = suffix
	}
	return name, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:n], nil
}
