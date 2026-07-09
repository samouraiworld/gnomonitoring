package database

import (
	"testing"
	"time"
)

// periodBounds must derive all bounds in UTC, independent of the process's
// local timezone, so the score partition's date-string boundaries line up with
// the UTC block timestamps stored in daily_participations / block_date.
func TestPeriodBounds_AllUTC(t *testing.T) {
	// A fixed instant expressed in a deliberately non-UTC zone. The same instant
	// in UTC is 2026-07-09T02:30:00Z (a Thursday) — note the local calendar day
	// (Jul 8, in UTC-5) differs from the UTC calendar day (Jul 9).
	minus5 := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 7, 8, 21, 30, 0, 0, minus5)

	for _, period := range []string{"current_week", "current_month", "current_year"} {
		start, end, err := periodBounds(period, now)
		if err != nil {
			t.Fatalf("%s: %v", period, err)
		}
		if start.Location() != time.UTC {
			t.Errorf("%s: start location = %v, want UTC", period, start.Location())
		}
		if end.Location() != time.UTC {
			t.Errorf("%s: end location = %v, want UTC", period, end.Location())
		}
	}
}

// periodBounds must return the same bounds for the same instant regardless of
// the location the caller's time.Time carries.
func TestPeriodBounds_TimezoneIndependent(t *testing.T) {
	instant := time.Date(2026, 7, 9, 2, 30, 0, 0, time.UTC)
	minus5 := time.FixedZone("UTC-5", -5*3600)

	for _, period := range []string{"last_24h", "current_week", "current_month", "current_year"} {
		s1, e1, err := periodBounds(period, instant)
		if err != nil {
			t.Fatalf("%s: %v", period, err)
		}
		s2, e2, err := periodBounds(period, instant.In(minus5))
		if err != nil {
			t.Fatalf("%s: %v", period, err)
		}
		if !s1.Equal(s2) || !e1.Equal(e2) {
			t.Errorf("%s: bounds differ by caller zone: UTC=[%s,%s) minus5=[%s,%s)",
				period, s1, e1, s2, e2)
		}
	}
}

// The UTC calendar day of the fixed instant (2026-07-09) must anchor the
// current_month/current_year bounds, not the caller's local calendar day.
func TestPeriodBounds_UsesUTCCalendarDay(t *testing.T) {
	minus5 := time.FixedZone("UTC-5", -5*3600)
	// 2026-07-01T01:00:00Z is still June 30 in UTC-5.
	now := time.Date(2026, 6, 30, 20, 0, 0, 0, minus5)

	start, _, err := periodBounds("current_month", now)
	if err != nil {
		t.Fatal(err)
	}
	// UTC instant is in July, so the month window must start on July 1 UTC.
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !start.Equal(want) {
		t.Errorf("current_month start = %s, want %s (UTC calendar day, not local)", start, want)
	}
}
