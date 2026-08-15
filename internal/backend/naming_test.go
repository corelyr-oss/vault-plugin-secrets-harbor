package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSegment(t *testing.T) {
	cases := map[string]string{
		"ci":                     "ci",
		"CI_Builder":             "ci-builder",
		"__weird--Name__":        "weird-name",
		"a.b_c-d":                "a-b-c-d",
		"ünïcödé":                "n-c-d",
		"!!!":                    "",
		"UPPER123":               "upper123",
		"spaces in name":         "spaces-in-name",
		strings.Repeat("x", 100): strings.Repeat("x", maxRoleSegment),
	}
	for in, want := range cases {
		require.Equal(t, want, normalizeSegment(in), "input %q", in)
	}
}

func TestRobotName_MatchesHarborRegex(t *testing.T) {
	inputs := []struct{ prefix, role string }{
		{"vault", "ci"},
		{"vault", "CI_Builder"},
		{"Vault-Prod", "k8s.pull_secret"},
		{"", "role"},
		{"vault", ""},
		{"", ""},
		{"!!!", "???"},
		{"vault", strings.Repeat("longrole-", 30)},
		{"vault", "-leading-and-trailing-"},
	}
	for _, in := range inputs {
		name, err := robotName(in.prefix, in.role)
		require.NoError(t, err)
		require.Regexp(t, harborRobotNameRe, name, "prefix=%q role=%q -> %q", in.prefix, in.role, name)
		require.LessOrEqual(t, len(name), maxRoleSegment*2+suffixHexLen+2)
	}
	n1, _ := robotName("vault", "ci")
	n2, _ := robotName("vault", "ci")
	require.NotEqual(t, n1, n2, "suffix must be random")
	require.True(t, strings.HasPrefix(n1, "vault-ci-"), n1)
}
