package backend

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTLToDays(t *testing.T) {
	cases := []struct {
		ttl  time.Duration
		want int64
	}{
		{0, 1},
		{-time.Hour, 1},
		{time.Second, 1},
		{time.Hour, 1},
		{24 * time.Hour, 1},
		{24*time.Hour + time.Second, 2},
		{100 * time.Hour, 5},
		{30 * 24 * time.Hour, 30},
	}
	for _, c := range cases {
		require.Equal(t, c.want, ttlToDays(c.ttl), "ttl=%s", c.ttl)
	}
}

func TestDurationForExpiry(t *testing.T) {
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, int64(1), durationForExpiry(created, created.Add(time.Hour)))
	require.Equal(t, int64(2), durationForExpiry(created, created.Add(25*time.Hour)))
	require.Equal(t, int64(11), durationForExpiry(created, created.Add(10*24*time.Hour+time.Minute)))
	// The resulting expiry always covers the wanted one.
	for _, want := range []time.Duration{time.Minute, 23 * time.Hour, 24 * time.Hour, 24*time.Hour + 1, 99 * time.Hour} {
		d := durationForExpiry(created, created.Add(want))
		require.False(t, created.AddDate(0, 0, int(d)).Before(created.Add(want)))
	}
}
