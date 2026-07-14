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

func TestComputePartition(t *testing.T) {
	// 2026-07-09T02:30:00Z, expressed in UTC-5 (still Jul 8 locally).
	minus5 := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 7, 8, 21, 30, 0, 0, minus5)
	todayUTC := "2026-07-09"
	// Aggregator fully caught up: yesterday (and everything before) is aggregated.
	caughtUp := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	t.Run("current_month includes agrega, raw starts today UTC", func(t *testing.T) {
		p, err := computePartition("current_month", now, caughtUp)
		if err != nil {
			t.Fatal(err)
		}
		if !p.includeAgrega {
			t.Fatalf("current_month should include agrega")
		}
		if p.agregaEnd != todayUTC {
			t.Fatalf("agregaEnd = %q, want %q", p.agregaEnd, todayUTC)
		}
		if p.agregaStart != "2026-07-01" {
			t.Fatalf("agregaStart = %q, want 2026-07-01", p.agregaStart)
		}
		wantRaw := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s", p.rawStart, wantRaw)
		}
	})

	t.Run("last_24h excludes agrega, raw is full window", func(t *testing.T) {
		p, err := computePartition("last_24h", now, caughtUp)
		if err != nil {
			t.Fatal(err)
		}
		if p.includeAgrega {
			t.Fatalf("last_24h must not include agrega")
		}
		wantRaw := time.Date(2026, 7, 9, 2, 30, 0, 0, time.UTC).Add(-24 * time.Hour)
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s", p.rawStart, wantRaw)
		}
	})

	t.Run("aggregator lagging two days: raw arm absorbs the gap, no missing day", func(t *testing.T) {
		// Aggregator hasn't rolled up anything since July 7: aggregatedThrough
		// (exclusive) is July 8.
		lagging := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
		p, err := computePartition("current_month", now, lagging)
		if err != nil {
			t.Fatal(err)
		}
		if !p.includeAgrega {
			t.Fatalf("current_month should still include agrega for July 1-7")
		}
		if p.agregaEnd != "2026-07-08" {
			t.Fatalf("agregaEnd = %q, want 2026-07-08 (only what's actually aggregated)", p.agregaEnd)
		}
		wantRaw := lagging
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s (raw arm must cover the un-aggregated gap)", p.rawStart, wantRaw)
		}
	})

	t.Run("nothing aggregated yet: raw arm covers the whole period", func(t *testing.T) {
		p, err := computePartition("current_month", now, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if p.includeAgrega {
			t.Fatalf("must not include agrega when nothing has been aggregated")
		}
		wantRaw := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		if !p.rawStart.Equal(wantRaw) {
			t.Fatalf("rawStart = %s, want %s", p.rawStart, wantRaw)
		}
	})
}

// PeriodElapsedDays must be pure time arithmetic (no DB access) and must
// floor to 1 day so a rate computed from it never spikes right at the start
// of an in-progress period.
func TestPeriodElapsedDays(t *testing.T) {
	t.Run("last_24h is always exactly 1", func(t *testing.T) {
		now := time.Date(2026, 7, 9, 2, 30, 0, 0, time.UTC)
		days, err := PeriodElapsedDays("last_24h", now)
		if err != nil {
			t.Fatal(err)
		}
		if days != 1 {
			t.Fatalf("days = %v, want 1", days)
		}
	})

	t.Run("current_month 10 minutes after month start floors to 1", func(t *testing.T) {
		now := time.Date(2026, 7, 1, 0, 10, 0, 0, time.UTC)
		days, err := PeriodElapsedDays("current_month", now)
		if err != nil {
			t.Fatal(err)
		}
		if days != 1 {
			t.Fatalf("days = %v, want 1 (floored)", days)
		}
	})

	t.Run("current_year at midpoint is about 182 days", func(t *testing.T) {
		now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
		days, err := PeriodElapsedDays("current_year", now)
		if err != nil {
			t.Fatal(err)
		}
		if days < 181 || days > 183 {
			t.Fatalf("days = %v, want ~182", days)
		}
	})

	t.Run("current_week partway through gives a value between 1 and 7", func(t *testing.T) {
		// 2026-07-09 is a Thursday; current_week starts the preceding Monday
		// (2026-07-06), so ~3 days have elapsed.
		now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
		days, err := PeriodElapsedDays("current_week", now)
		if err != nil {
			t.Fatal(err)
		}
		if days < 3 || days > 4 {
			t.Fatalf("days = %v, want ~3.5 (Thursday noon, week started Monday)", days)
		}
	})

	t.Run("invalid period returns an error", func(t *testing.T) {
		_, err := PeriodElapsedDays("bogus", time.Now())
		if err == nil {
			t.Fatal("want error for invalid period, got nil")
		}
	})
}
