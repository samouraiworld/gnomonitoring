package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"gorm.io/gorm"
)

func TestGetAggregatedThrough(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test16"

	t.Run("nothing aggregated yet returns zero time", func(t *testing.T) {
		got, err := database.GetAggregatedThrough(db, chain)
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsZero() {
			t.Fatalf("want zero time, got %s", got)
		}
	})

	t.Run("returns the day after the latest aggregated block_date", func(t *testing.T) {
		if err := db.Exec(`INSERT INTO daily_participation_agregas
			(chain_id, addr, block_date, moniker, participated_count, missed_count,
			 tx_contribution_count, total_blocks, first_block_height, last_block_height)
			VALUES (?, 'addrA', '2026-07-05', 'A', 10, 0, 0, 10, 1, 10)`, chain).Error; err != nil {
			t.Fatal(err)
		}
		got, err := database.GetAggregatedThrough(db, chain)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("got %s, want %s", got, want)
		}
	})
}

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

func TestGetValidatorScoresExcludesAddrAll(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	inMonth := time.Date(now.Year(), now.Month(), 2, 12, 0, 0, 0, time.UTC)

	seedScoreAlert(t, db, "test12", "g1aaa", "CRITICAL", 100, 130, inMonth)
	// Chain-wide "blockchain stuck" alert — must never appear as a fake validator.
	seedScoreAlert(t, db, "test12", "all", "CRITICAL", 1, 999, inMonth)

	rows, err := database.GetValidatorScores(db, "test12", "current_month")
	if err != nil {
		t.Fatalf("GetValidatorScores: %v", err)
	}
	for _, r := range rows {
		if r.Addr == "all" {
			t.Fatalf("addr='all' must be excluded from GetValidatorScores, got: %+v", rows)
		}
	}
	if len(rows) != 1 || rows[0].Addr != "g1aaa" {
		t.Fatalf("want exactly [g1aaa], got %+v", rows)
	}
}

func TestGetValidatorScoresIncidentCount(t *testing.T) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(now.Year(), now.Month(), 2, 9, 0, 0, 0, time.UTC)

	t.Run("escalation without RESOLVED is one incident", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "g1esc", "WARNING", 10, 15, day2)
		seedScoreAlert(t, db, "test20", "g1esc", "CRITICAL", 10, 40, day2.Add(time.Hour))

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 1 {
			t.Fatalf("want 1 row with IncidentCount=1, got %+v", rows)
		}
	})

	t.Run("RESOLVED then new WARNING is two incidents", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "g1flap", "WARNING", 10, 15, day2)
		seedScoreAlert(t, db, "test20", "g1flap", "RESOLVED", 10, 15, day2.Add(time.Hour))
		seedScoreAlert(t, db, "test20", "g1flap", "WARNING", 20, 25, day2.Add(2*time.Hour))

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 2 {
			t.Fatalf("want 1 row with IncidentCount=2, got %+v", rows)
		}
	})

	t.Run("incident started before the period and still resending counts zero new", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		// Started before this month, no RESOLVED yet, resent inside the period.
		seedScoreAlert(t, db, "test20", "g1cont", "CRITICAL", 10, 40, monthStart.Add(-time.Hour))
		seedScoreAlert(t, db, "test20", "g1cont", "CRITICAL", 10, 400, day2)

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 1 || rows[0].IncidentCount != 0 {
			t.Fatalf("want 1 row with IncidentCount=0 (continuation, not new), got %+v", rows)
		}
		// critical_count must still count the resend row itself — unaffected by freq.
		if rows[0].CriticalCount != 1 {
			t.Fatalf("critical_count must stay a raw resend count, got %d", rows[0].CriticalCount)
		}
	})

	t.Run("addr=all and other chains never counted", func(t *testing.T) {
		db := testoutils.NewTestDB(t)
		seedScoreAlert(t, db, "test20", "all", "CRITICAL", 1, 999, day2)
		seedScoreAlert(t, db, "other20", "g1esc", "CRITICAL", 1, 999, day2)

		rows, err := database.GetValidatorScores(db, "test20", "current_month")
		if err != nil {
			t.Fatalf("GetValidatorScores: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("want no rows (addr=all excluded, other chain excluded), got %+v", rows)
		}
	})
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

	rows, _, err := database.GetValidatorParticipation(db, chain, "current_month")
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

// TestGetValidatorParticipation_AggregatorLagIncludesUnaggregatedDay guards
// against the bug where a day not yet rolled up by the (hourly) aggregator
// vanished from the report: neither counted by the raw arm (which started at
// wall-clock "today") nor by the agrega arm (which hadn't been written yet).
func TestGetValidatorParticipation_AggregatorLagIncludesUnaggregatedDay(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test17"

	// Aggregator is lagging: only two days ago has been rolled up.
	twoDaysAgo := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	if err := db.Exec(`INSERT INTO daily_participation_agregas
		(chain_id, addr, block_date, moniker, participated_count, missed_count,
		 tx_contribution_count, total_blocks, first_block_height, last_block_height)
		VALUES (?, 'addrA', ?, 'A', 10, 0, 0, 10, 1, 10)`,
		chain, twoDaysAgo).Error; err != nil {
		t.Fatal(err)
	}

	// Yesterday's raw rows: NOT yet aggregated (aggregator lagging).
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	yesterdayTS := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 6, 0, 0, 0, time.UTC)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 501, 'A', 'addrA', true, false)`,
		chain, yesterdayTS).Error; err != nil {
		t.Fatal(err)
	}

	// Today's raw row.
	now := time.Now().UTC()
	todayTS := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, time.UTC)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution)
		VALUES (?, ?, 502, 'A', 'addrA', true, false)`,
		chain, todayTS).Error; err != nil {
		t.Fatal(err)
	}

	rows, _, err := database.GetValidatorParticipation(db, chain, "current_month")
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
	// 10 (aggregated, two days ago) + 1 (yesterday, raw, not yet aggregated) +
	// 1 (today, raw) = 12, with no double count and no dropped day even though
	// the aggregator hasn't caught up on yesterday yet.
	if got.SignedBlocks != 12 || got.TotalBlocks != 12 {
		t.Fatalf("signed/total = %d/%d, want 12/12 (yesterday must not be dropped nor double-counted)", got.SignedBlocks, got.TotalBlocks)
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
	rows, _, err := database.GetValidatorParticipation(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SignedBlocks != 2 || rows[0].TotalBlocks != 3 {
		t.Fatalf("got %+v, want one row 2/3", rows)
	}
}

func TestGetValidatorVP(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	for _, r := range []struct {
		addr string
		vp   int64
	}{{"a", 100}, {"b", 300}, {"c", 0}} {
		if err := database.UpsertAddrMoniker(db, chain, r.addr, r.addr); err != nil {
			t.Fatal(err)
		}
		if err := database.UpsertAddrMonikerVP(db, chain, r.addr, r.vp); err != nil {
			t.Fatal(err)
		}
	}
	perAddr, monikerByAddr, sum, max, err := database.GetValidatorVP(db, chain)
	if err != nil {
		t.Fatal(err)
	}
	if perAddr["b"] != 300 || sum != 400 || max != 300 {
		t.Fatalf("perAddr=%v sum=%d max=%d, want b=300 sum=400 max=300", perAddr, sum, max)
	}
	if monikerByAddr["b"] != "b" {
		t.Fatalf("monikerByAddr[b]=%q, want %q", monikerByAddr["b"], "b")
	}
}

func TestGetValidatorParticipation_ChainBlocks(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	now := time.Now().UTC()
	rec := now.Add(-30 * time.Minute)
	if err := db.Exec(`INSERT INTO daily_participations
		(chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed)
		VALUES (?, ?, 1, 'A', 'a', true, false, true),
		       (?, ?, 1, 'B', 'b', true, false, false),
		       (?, ?, 2, 'A', 'a', true, false, false)`,
		chain, rec, chain, rec, chain, rec).Error; err != nil {
		t.Fatal(err)
	}
	// Two distinct block heights in the window.
	_, chainBlocks, err := database.GetValidatorParticipation(db, chain, "last_24h")
	if err != nil {
		t.Fatal(err)
	}
	if chainBlocks != 2 {
		t.Fatalf("chain blocks = %d, want 2 (distinct heights)", chainBlocks)
	}
}

func TestGetLastAlertTimes(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test13"
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	// validator a: a WARNING then a later CRITICAL → last = the CRITICAL time.
	seedScoreAlert(t, db, chain, "a", "WARNING", 0, 0, base)
	seedScoreAlert(t, db, chain, "a", "CRITICAL", 0, 0, base.Add(2*time.Hour))
	// validator b: only a RESOLVED row → not an alert, must be absent.
	seedScoreAlert(t, db, chain, "b", "RESOLVED", 0, 0, base.Add(time.Hour))
	// chain-wide "stuck" alert → excluded (not an individual validator).
	seedScoreAlert(t, db, chain, "all", "CRITICAL", 0, 0, base.Add(3*time.Hour))
	// another chain → must not leak.
	seedScoreAlert(t, db, "otherchain", "a", "CRITICAL", 0, 0, base.Add(10*time.Hour))

	m, err := database.GetLastAlertTimes(db, chain)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m["a"]
	if !ok || !got.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("a last alert = %v (ok=%v), want %v", got, ok, base.Add(2*time.Hour))
	}
	if _, ok := m["b"]; ok {
		t.Fatalf("b has only RESOLVED, must be absent")
	}
	if _, ok := m["all"]; ok {
		t.Fatalf("addr 'all' must be excluded")
	}
	if len(m) != 1 {
		t.Fatalf("want exactly 1 entry (a), got %d: %v", len(m), m)
	}
}
