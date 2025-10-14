package gnovalidator_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestSaveParticipation2(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Simuler un map de participation
	monikerMap := map[string]string{
		"addr1": "Validator1",
		"addr2": "Validator2",
	}

	blockTime := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)

	participating := map[string]gnovalidator.Participation{
		"addr1": {Participated: true, Timestamp: blockTime, TxContribution: true},
		"addr2": {Participated: false, Timestamp: blockTime, TxContribution: false},
	}

	// call SavePArticipation
	err := gnovalidator.SaveParticipation(db, 100, participating, monikerMap, blockTime)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	// Check data save
	var participations []database.DailyParticipation
	result := db.Where("block_height = ?", 100).Find(&participations)
	if result.Error != nil {
		t.Fatalf("Error querying participations: %v", result.Error)
	}
	if len(participations) != 2 {
		t.Fatalf("Expected 2 participations, got %d", len(participations))
	}

	for _, p := range participations {
		if p.Addr == "addr1" && !p.Participated {
			t.Errorf("Expected addr1 to have participated")
		}
		if p.Addr == "addr2" && p.Participated {
			t.Errorf("Expected addr2 to not have participated")
		}
	}
}
