package gnovalidator_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

func TestCalculateRate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	date := "2025-07-14"
	stmt := `INSERT INTO daily_participation(date, block_height, moniker, addr, participated) VALUES (?, ?, ?, ?, ?)`
	entries := []struct {
		height      int
		participate bool
	}{
		{100, true}, {101, false}, {102, true},
	}

	for _, e := range entries {
		_, err := db.Exec(stmt, date, e.height, "MonikerTest", "validator1", e.participate)
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}
	}

	rates, min, max := gnovalidator.CalculateRate(db, date)

	if len(rates) != 1 {
		t.Errorf("expected 1 validator, got %d", len(rates))
	}
	if rate := rates["validator1"]; rate < 66.6 || rate > 66.7 {
		t.Errorf("unexpected rate: got %.2f, want ~66.67", rate)
	}
	if min != 100 || max != 102 {
		t.Errorf("unexpected block range: min=%d max=%d", min, max)
	}
}
func TestPruneOldParticipationData(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	oldDate := time.Now().AddDate(0, 0, -40).Format("2006-01-02")
	recentDate := time.Now().Format("2006-01-02")
	stmt := `INSERT INTO daily_participation(date, block_height, moniker, addr, participated) VALUES (?, ?, ?, ?, ?)`

	_, _ = db.Exec(stmt, oldDate, 1, "Old", "val1", true)
	_, _ = db.Exec(stmt, recentDate, 2, "Recent", "val2", true)

	err := gnovalidator.PruneOldParticipationData(db, 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM daily_participation WHERE date = ?`, oldDate).Scan(&count)
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Errorf("old data not pruned")
	}
}
func TestGetLastStoredHeight(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	stmt := `INSERT INTO daily_participation(date, block_height, moniker, addr, participated) VALUES (?, ?, ?, ?, ?)`
	_, _ = db.Exec(stmt, "2025-07-14", 42, "Moniker", "validator1", true)

	height, err := gnovalidator.GetLastStoredHeight(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if height != 42 {
		t.Errorf("expected height 42, got %d", height)
	}
}
