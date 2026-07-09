package database_test

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestUpsertAddrMonikerVP(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if err := database.UpsertAddrMoniker(db, "test13", "addrX", "X"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAddrMonikerVP(db, "test13", "addrX", 500); err != nil {
		t.Fatal(err)
	}
	var vp int64
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "addrX").Scan(&vp).Error; err != nil {
		t.Fatal(err)
	}
	if vp != 500 {
		t.Fatalf("voting_power = %d, want 500", vp)
	}
}
