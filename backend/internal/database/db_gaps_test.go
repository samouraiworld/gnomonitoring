package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestFindMissingHeights_ReturnsOnlyGapHeights(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "gaptest"
	today := time.Now().UTC()
	day := time.Date(today.Year(), today.Month(), today.Day(), 12, 0, 0, 0, time.UTC)

	// Heights 100..104 recorded, then a gap at 105..107, then 108..110 recorded.
	present := []int64{100, 101, 102, 103, 104, 108, 109, 110}
	for _, h := range present {
		row := database.DailyParticipation{
			ChainID:      chain,
			Addr:         "g1addr",
			Moniker:      "validator-a",
			BlockHeight:  h,
			Date:         day,
			Participated: true,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed height %d: %v", h, err)
		}
	}

	since := today.AddDate(0, 0, -7)
	missing, err := database.FindMissingHeights(db, chain, since)
	if err != nil {
		t.Fatalf("FindMissingHeights error: %v", err)
	}

	want := []int64{105, 106, 107}
	if len(missing) != len(want) {
		t.Fatalf("got %v missing heights, want %v", missing, want)
	}
	for i, h := range want {
		if missing[i] != h {
			t.Fatalf("got %v missing heights, want %v", missing, want)
		}
	}
}

func TestFindMissingHeights_NoGapReturnsEmpty(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "gaptest-clean"
	today := time.Now().UTC()
	day := time.Date(today.Year(), today.Month(), today.Day(), 12, 0, 0, 0, time.UTC)

	for _, h := range []int64{1, 2, 3} {
		row := database.DailyParticipation{
			ChainID:      chain,
			Addr:         "g1addr",
			Moniker:      "validator-a",
			BlockHeight:  h,
			Date:         day,
			Participated: true,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed height %d: %v", h, err)
		}
	}

	since := today.AddDate(0, 0, -7)
	missing, err := database.FindMissingHeights(db, chain, since)
	if err != nil {
		t.Fatalf("FindMissingHeights error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("got %v, want no missing heights", missing)
	}
}
