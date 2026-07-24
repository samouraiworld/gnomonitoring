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
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", rows, -1); err != nil {
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

func TestUpsertAddrMonikerVPBatch_SetsFirstActiveBlockOnlyOnFirstInsert(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// "a" is brand new: first_active_block must be set to the join-height
	// snapshot (5929), the best available proxy for "block it joined the
	// valset" — this is what lets a validator that never signs a single
	// block still accumulate real missed-block history instead of being
	// skipped forever as "activation unknown".
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}, 5929); err != nil {
		t.Fatal(err)
	}
	var fab int64
	if err := db.Raw(`SELECT first_active_block FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Scan(&fab).Error; err != nil {
		t.Fatal(err)
	}
	if fab != 5929 {
		t.Fatalf("first_active_block = %d, want 5929 (the join-height snapshot)", fab)
	}

	// A later poll re-upserts VP for the same, already-known "a" at a much
	// later height. first_active_block must NOT be overwritten — it records
	// the FIRST time we ever saw this address, not the most recent poll.
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 150},
	}, 999999); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT first_active_block FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Scan(&fab).Error; err != nil {
		t.Fatal(err)
	}
	if fab != 5929 {
		t.Fatalf("first_active_block changed to %d after a later poll, want it to stay 5929", fab)
	}
	var vp int64
	if err := db.Raw(`SELECT voting_power FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Scan(&vp).Error; err != nil {
		t.Fatal(err)
	}
	if vp != 150 {
		t.Fatalf("voting_power = %d, want 150 (must still update on conflict)", vp)
	}
}

func TestUpsertAddrMonikerVPBatch_UnknownJoinHeightFallsBackToSentinel(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}, -1); err != nil {
		t.Fatal(err)
	}
	var fab int64
	if err := db.Raw(`SELECT first_active_block FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Scan(&fab).Error; err != nil {
		t.Fatal(err)
	}
	if fab != -1 {
		t.Fatalf("first_active_block = %d, want -1 when join height is unknown", fab)
	}
}

func TestUpsertFirstActiveBlock_LowersFromNull(t *testing.T) {
	db := testoutils.NewTestDB(t)
	// A row left over from before UpsertFirstActiveBlock's WHERE clause
	// matched NULL (a past migration bug) — simulate that stuck state
	// directly, since no production code path writes NULL anymore.
	if err := db.Exec(`INSERT INTO addr_monikers (chain_id, addr, moniker, first_active_block) VALUES (?, ?, '', NULL)`,
		"test13", "a").Error; err != nil {
		t.Fatal(err)
	}

	// A true participation at height 500 must still be able to record the
	// real activation, even though first_active_block is NULL rather than
	// the usual -1 sentinel.
	if err := database.UpsertFirstActiveBlock(db, "test13", "a", 500); err != nil {
		t.Fatal(err)
	}
	var fab int64
	if err := db.Raw(`SELECT first_active_block FROM addr_monikers WHERE chain_id=? AND addr=?`,
		"test13", "a").Scan(&fab).Error; err != nil {
		t.Fatal(err)
	}
	if fab != 500 {
		t.Fatalf("first_active_block = %d, want 500 (must be set from a stuck NULL state)", fab)
	}
}

func TestZeroDepartedVotingPower(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// b left the valset (not in currentAddrs); a and c are still bonded.
	if err := database.UpsertAddrMonikerVPBatch(db, "test13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
		{Addr: "b", VotingPower: 50},
		{Addr: "c", VotingPower: 10},
	}, -1); err != nil {
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
	}, -1); err != nil {
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
	}, -1); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAddrMonikerVPBatch(db, "other13", []database.AddrVP{
		{Addr: "a", VotingPower: 100},
	}, -1); err != nil {
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
