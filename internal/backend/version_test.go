package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"v1.2.3":            "v1.2.3",
		"1.2.3":             "1.2.3",
		"v1.2.3-rc1":        "v1.2.3-rc1",
		"v1.2.3+build.5":    "v1.2.3+build.5",
		"dev":               "v0.0.0-dev",
		"abc1234-dirty":     "v0.0.0-abc1234-dirty",
		"v0.1.0-3-gabc1234": "v0.1.0-3-gabc1234",
		"###":               "v0.0.0-dev",
	}
	for in, want := range cases {
		require.Equal(t, want, normalizeVersion(in), "input %q", in)
	}
}
