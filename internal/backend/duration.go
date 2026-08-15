package backend

import (
	"math"
	"time"
)

const day = 24 * time.Hour

// ttlToDays converts a lease duration to Harbor's robot `duration` (days),
// rounding up and never returning less than 1.
func ttlToDays(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 1
	}
	d := int64(math.Ceil(float64(ttl) / float64(day)))
	if d < 1 {
		d = 1
	}
	return d
}

// durationForExpiry returns the Harbor `duration` (days from creationTime)
// needed so that the robot expires at or after wantExpiry.
func durationForExpiry(creationTime, wantExpiry time.Time) int64 {
	return ttlToDays(wantExpiry.Sub(creationTime))
}
