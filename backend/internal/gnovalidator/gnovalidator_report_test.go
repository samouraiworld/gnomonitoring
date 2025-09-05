package gnovalidator_test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testutils"
	"gorm.io/gorm"
)

func insertParticipationData(db *gorm.DB, date string, addr string, moniker string, participated []bool, startHeight int) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	stmt := `
		INSERT INTO daily_participations (date, block_height, addr, moniker, participated)
		VALUES (?, ?, ?, ?, ?)
	`

	for i, p := range participated {
		blockHeight := startHeight + i
		if err := tx.Exec(stmt, date, blockHeight, addr, moniker, p).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed at height %d: %w", blockHeight, err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}

	return nil
}

func TestCalculateRate(t *testing.T) {
	db := testutils.NewTestDB(t)

	date := time.Now().Format("2006-01-02")

	err := insertParticipationData(db, date, "valoper1", "validator-one", []bool{true, false, true, true}, 100)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}
	err = insertParticipationData(db, date, "valoper2", "validator-two", []bool{false, false, false, false}, 200)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}

	rates, min, max := gnovalidator.CalculateRate(db, date)

	if len(rates) != 2 {
		t.Errorf("Expected 2 validators, got %d", len(rates))
	}

	if rate := rates["valoper1"]; rate != 75.0 {
		t.Errorf("Expected rate 75.0 for valoper1, got %.2f", rate)
	}
	if rate := rates["valoper2"]; rate != 0.0 {
		t.Errorf("Expected rate 0.0 for valoper2, got %.2f", rate)
	}

	if min != 100 {
		t.Errorf("Expected min block 100, got %d", min)
	}
	if max != 203 {
		t.Errorf("Expected max block 203, got %d", max)
	}
}
