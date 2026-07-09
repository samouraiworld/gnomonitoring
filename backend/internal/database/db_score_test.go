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

func seedParticipation(t *testing.T, db *gorm.DB, chain, addr, moniker string, height int64, date time.Time) {
	t.Helper()
	row := database.DailyParticipation{
		ChainID: chain, Addr: addr, Moniker: moniker,
		BlockHeight: height, Date: date, Participated: true, TxContribution: true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed participation: %v", err)
	}
}

func TestGetChainValidators(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	seedParticipation(t, db, "test12", "g1aaa", "alpha", 100, now)
	seedParticipation(t, db, "test12", "g1bbb", "beta-row", 101, now)
	// moniker override via addr_monikers should win over the participation row's moniker.
	if err := db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1bbb", Moniker: "beta-override"}).Error; err != nil {
		t.Fatalf("seed addr_moniker: %v", err)
	}
	// other chain must not leak in
	seedParticipation(t, db, "other", "g1ccc", "gamma", 50, now)

	rows, err := database.GetChainValidators(db, "test12")
	if err != nil {
		t.Fatalf("GetChainValidators: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 validators, got %d: %+v", len(rows), rows)
	}
	if rows[0].Addr != "g1aaa" || rows[1].Addr != "g1bbb" {
		t.Fatalf("unexpected ordering: %+v", rows)
	}
	if rows[0].Moniker != "alpha" {
		t.Fatalf("want moniker fallback alpha, got %q", rows[0].Moniker)
	}
	if rows[1].Moniker != "beta-override" {
		t.Fatalf("want moniker override beta-override, got %q", rows[1].Moniker)
	}
}

func TestGetValidatorParticipation_UnionAgregaAndToday(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"

	// Aggregated complete day (yesterday): 80 signed / 100 total for addrA.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if err := db.Exec(`INSERT INTO daily_participation_agregas
		(chain_id, addr, block_date, moniker, participated_count, missed_count,
		 tx_contribution_count, total_blocks, first_block_height, last_block_height)
		VALUES (?, 'addrA', ?, 'A', 80, 20, 0, 100, 1, 100)`,
		chain, yesterday).Error; err != nil {
		t.Fatal(err)
	}

	// Today's raw rows for addrA: 2 blocks, 1 signed.
	now := time.Now().UTC()
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, time.UTC)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 201, 'A', 'addrA', true, false),
		       (?, ?, 202, 'A', 'addrA', false, false)`,
		chain, today0, chain, today0).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := database.GetValidatorParticipation(db, chain, "current_month")
	if err != nil {
		t.Fatal(err)
	}
	var got *database.ParticipationRaw
	for i := range rows {
		if rows[i].Addr == "addrA" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("addrA missing from results")
	}
	// 80 + 1 signed, 100 + 2 total.
	if got.SignedBlocks != 81 || got.TotalBlocks != 102 {
		t.Fatalf("signed/total = %d/%d, want 81/102", got.SignedBlocks, got.TotalBlocks)
	}
}

func TestGetValidatorParticipation_Last24hRawOnly(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 301, 'B', 'addrB', true, false),
		       (?, ?, 302, 'B', 'addrB', true, false),
		       (?, ?, 303, 'B', 'addrB', false, false)`,
		chain, recent, chain, recent, chain, recent).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := database.GetValidatorParticipation(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SignedBlocks != 2 || rows[0].TotalBlocks != 3 {
		t.Fatalf("got %+v, want one row 2/3", rows)
	}
}
