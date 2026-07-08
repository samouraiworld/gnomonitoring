package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"gorm.io/gorm"
)

func seedScoreAlert(t *testing.T, db *gorm.DB, chain, addr, level string, start, end int64, sentAt time.Time) {
	t.Helper()
	row := database.AlertLog{
		ChainID: chain, Addr: addr, Level: level,
		StartHeight: start, EndHeight: end, Moniker: addr + "-mon",
		SentAt: sentAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed alert: %v", err)
	}
}

func TestGetValidatorScoresCurrentMonth(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	inMonth := time.Date(now.Year(), now.Month(), 2, 12, 0, 0, 0, time.UTC)

	seedScoreAlert(t, db, "test12", "g1aaa", "CRITICAL", 100, 130, inMonth)
	seedScoreAlert(t, db, "test12", "g1aaa", "WARNING", 90, 95, inMonth)
	// other chain must not leak in
	seedScoreAlert(t, db, "other", "g1aaa", "CRITICAL", 1, 999, inMonth)

	rows, err := database.GetValidatorScores(db, "test12", "current_month")
	if err != nil {
		t.Fatalf("GetValidatorScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 validator, got %d", len(rows))
	}
	got := rows[0]
	if got.Addr != "g1aaa" || got.CriticalCount != 1 || got.WarningCount != 1 || got.DowntimeBlocks != 30 {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.Moniker != "g1aaa-mon" {
		t.Fatalf("want moniker g1aaa-mon, got %q", got.Moniker)
	}
}

func TestGetValidatorScoresInvalidPeriod(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if _, err := database.GetValidatorScores(db, "test12", "nope"); err == nil {
		t.Fatal("expected error for invalid period")
	}
}
