package database_test

import (
	"context"
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

	// 350 rows spans two chunks (chunk size = 990/3 = 330).
	rows := make([]database.AddrVP, 0, 350)
	rows = append(rows, database.AddrVP{Addr: "a", VotingPower: 100})
	for i := 1; i < 350; i++ {
		rows = append(rows, database.AddrVP{Addr: fmt.Sprintf("v%03d", i), VotingPower: int64(i)})
	}
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", rows); err != nil {
		t.Fatal(err)
	}

	var vpA, vp349 int64
	var monA string
	if err := db.Raw(`SELECT voting_power, moniker FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Row().Scan(&vpA, &monA); err != nil {
		t.Fatal(err)
	}
	if vpA != 100 || monA != "alpha" {
		t.Fatalf("addr a: vp=%d moniker=%q, want 100/alpha", vpA, monA)
	}
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "v349").Scan(&vp349).Error; err != nil {
		t.Fatal(err)
	}
	if vp349 != 349 {
		t.Fatalf("addr v349: vp=%d, want 349 (second chunk)", vp349)
	}
}

func TestZeroDepartedVotingPower(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// b left the valset (not in currentAddrs); a and c are still bonded.
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
		{Addr: "b", VotingPower: 50},
		{Addr: "c", VotingPower: 10},
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.ZeroDepartedVotingPower(context.Background(), db, "test13", []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}

	var vpA, vpB, vpC int64
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "test13", "a").Scan(&vpA)
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "test13", "b").Scan(&vpB)
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "test13", "c").Scan(&vpC)
	if vpA != 100 || vpC != 10 {
		t.Fatalf("still-bonded addrs must keep their VP: a=%d c=%d, want 100/10", vpA, vpC)
	}
	if vpB != 0 {
		t.Fatalf("departed addr b must be zeroed, got %d", vpB)
	}
}

func TestZeroDepartedVotingPower_EmptyCurrentAddrsIsNoop(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}); err != nil {
		t.Fatal(err)
	}
	// An empty currentAddrs list must never zero out everyone (e.g. a
	// transient empty /validators response) — it's a deliberate no-op.
	if err := database.ZeroDepartedVotingPower(context.Background(), db, "test13", nil); err != nil {
		t.Fatal(err)
	}
	var vpA int64
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "test13", "a").Scan(&vpA)
	if vpA != 100 {
		t.Fatalf("empty currentAddrs must be a no-op, got vp=%d want 100", vpA)
	}
}

func TestZeroDepartedVotingPower_ChainScoped(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAddrMonikerVPBatch(db, "other13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}); err != nil {
		t.Fatal(err)
	}
	// "a" left test13's valset (currentAddrs excludes it) but is still bonded
	// on other13 — the zero-out must not leak across chains.
	if err := database.ZeroDepartedVotingPower(context.Background(), db, "test13", []string{"someoneelse"}); err != nil {
		t.Fatal(err)
	}
	var vpTest13, vpOther int64
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "test13", "a").Scan(&vpTest13)
	db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`, "other13", "a").Scan(&vpOther)
	if vpTest13 != 0 {
		t.Fatalf("test13's departed addr must be zeroed, got %d want 0", vpTest13)
	}
	if vpOther != 100 {
		t.Fatalf("other chain's voting_power must be untouched, got %d want 100", vpOther)
	}
}
