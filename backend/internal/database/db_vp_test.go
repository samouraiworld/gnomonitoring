package database_test

import (
	"fmt"
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

func TestUpsertAddrMonikerVPBatch(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Pre-existing moniker row must keep its moniker; VP updates in place.
	if err := database.UpsertAddrMoniker(db, "test13", "a", "alpha"); err != nil {
		t.Fatal(err)
	}

	// 300 rows crosses the chunk boundary (>247).
	rows := make([]database.AddrVP, 0, 300)
	rows = append(rows, database.AddrVP{Addr: "a", VotingPower: 100})
	for i := 1; i < 300; i++ {
		rows = append(rows, database.AddrVP{Addr: fmt.Sprintf("v%03d", i), VotingPower: int64(i)})
	}
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", rows); err != nil {
		t.Fatal(err)
	}

	var vpA, vp299 int64
	var monA string
	if err := db.Raw(`SELECT voting_power, moniker FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Row().Scan(&vpA, &monA); err != nil {
		t.Fatal(err)
	}
	if vpA != 100 || monA != "alpha" {
		t.Fatalf("addr a: vp=%d moniker=%q, want 100/alpha", vpA, monA)
	}
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "v299").Scan(&vp299).Error; err != nil {
		t.Fatal(err)
	}
	if vp299 != 299 {
		t.Fatalf("addr v299: vp=%d, want 299 (chunk boundary)", vp299)
	}
}
