package gnovalidator_test

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect database: %v", err)
	}

	// Auto migrate ta struct
	err = db.AutoMigrate(
		&database.User{}, &database.AlertContact{}, &database.WebhookValidator{},
		&database.WebhookGovDAO{}, &database.HourReport{}, &database.GovDAOState{},
		&database.DailyParticipation{}, &database.AlertLog{}, &database.AddrMoniker{},
	)

	return db
}
func TestSaveParticipation(t *testing.T) {
	db := setupTestDB(t)

	// Simuler un map de participation
	monikerMap := map[string]string{
		"addr1": "Validator1",
		"addr2": "Validator2",
	}
	participating := map[string]bool{
		"addr1": true,
		"addr2": false,
	}

	// Appeler ta fonction
	err := gnovalidator.SaveParticipation(db, 100, participating, monikerMap)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	// Vérifier que les données sont bien enregistrées
	var participations []database.DailyParticipation
	result := db.Find(&participations)
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
