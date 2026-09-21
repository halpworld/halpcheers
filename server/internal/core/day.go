package core

import (
	"strconv"
	"time"
)

// Day represents days elapsed since the Unix epoch (1970-01-01 UTC).
//
// In accordance with Halp privacy rules (docs/PRIVACY.md § Timestamps),
// all timestamps persisted to disk must be Day integers rather than
// second-precision timestamps. This limits cross-table correlation fingerprints.
type Day int32

const secondsPerDay = 86400

// TimeToDay converts a standard time.Time to a Day in UTC.
func TimeToDay(t time.Time) Day {
	return Day(t.UTC().Unix() / secondsPerDay)
}

// Today returns the current day since epoch in UTC.
func Today() Day {
	return TimeToDay(time.Now())
}

// DayFromInt converts an int64 integer from SQLite to Day.
func DayFromInt(v int64) Day {
	return Day(v)
}

// Int returns the Day value as an int64 for SQLite query parameters.
func (d Day) Int() int64 {
	return int64(d)
}

// Time converts the Day to a time.Time at 00:00:00 UTC on that day.
func (d Day) Time() time.Time {
	return time.Unix(int64(d)*secondsPerDay, 0).UTC()
}

// AddDays returns a new Day offset by n days.
func (d Day) AddDays(n int) Day {
	return Day(int(d) + n)
}

// Sub returns the number of days between d and other (d - other).
func (d Day) Sub(other Day) int {
	return int(d - other)
}

// String returns the string representation of Day.
func (d Day) String() string {
	return strconv.Itoa(int(d))
}
