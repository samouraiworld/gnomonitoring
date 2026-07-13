package database_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInitDB_BadDSN_ReturnsError(t *testing.T) {
	db, err := database.InitDB("host=127.0.0.1 port=1 user=nobody dbname=nobody sslmode=disable connect_timeout=1")
	require.Error(t, err, "InitDB with unreachable DSN should return an error, not call log.Fatalf")
	require.Nil(t, db)
}

func TestIndexMigration(t *testing.T) {
	db := testoutils.NewTestDB(t) // NewTestDB runs InitDB migrations

	rows, err := db.Raw(`
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'daily_participations'
	`).Rows()
	require.NoError(t, err)
	defer rows.Close()

	present := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		present[name] = true
	}

	// Dropped:
	require.False(t, present["idx_dp_chain_addr_blockheight"], "duplicate of unique index should be dropped")
	require.False(t, present["idx_dp_chain_addr"], "prefix index should be dropped")
	require.False(t, present["idx_dp_chain_date"], "prefix index should be dropped")
	// Kept:
	require.True(t, present["idx_dp_chain_block_height"])
	require.True(t, present["idx_dp_chain_addr_participated"])
	require.True(t, present["idx_dp_chain_date_addr"])
	// Added (partial):
	require.True(t, present["idx_dp_chain_addr_missed"])
	require.True(t, present["idx_dp_chain_addr_active"])
}

func TestDailyParticipationsVacuumTuning(t *testing.T) {
	db := testoutils.NewTestDB(t)

	var reloptions sql.NullString
	// Scope to the test's own schema: pg_class is not schema-qualified, so under
	// the parallel suite multiple schemas each have a daily_participations table.
	err := db.Raw(`
		SELECT array_to_string(c.reloptions, ',')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'daily_participations' AND n.nspname = current_schema()
	`).Scan(&reloptions).Error
	require.NoError(t, err)
	require.True(t, reloptions.Valid)
	require.Contains(t, reloptions.String, "autovacuum_vacuum_scale_factor=0.02")
}

func TestApplyMultiChainMigrations_SchemaScoped(t *testing.T) {
	db1 := testoutils.NewTestDB(t)
	db2 := testoutils.NewTestDB(t)

	require.NoError(t, database.ApplyMultiChainMigrations(db1))
	require.NoError(t, database.ApplyMultiChainMigrations(db2))
	require.NoError(t, database.ApplyTelegramChainIDMigration(db1))
	require.NoError(t, database.ApplyTelegramChainIDMigration(db2))
}

func TestInitDB_SessionTimeZoneIsUTC(t *testing.T) {
	db := testoutils.NewTestDB(t)
	var tz string
	require.NoError(t, db.Raw("SHOW TimeZone").Scan(&tz).Error)
	require.Equal(t, "UTC", tz)
}

func seedDP(t *testing.T, db *gorm.DB, chain, addr string, height int64, participated bool, date time.Time) {
	t.Helper()
	row := database.DailyParticipation{
		ChainID: chain, Addr: addr, Moniker: addr + "-mon",
		BlockHeight: height, Date: date, Participated: participated, TxContribution: false,
	}
	require.NoError(t, db.Create(&row).Error)
}

func seedAgrega(t *testing.T, db *gorm.DB, chain, addr, blockDate string, participatedCount int, firstHeight, lastHeight int64) {
	t.Helper()
	row := database.DailyParticipationAgrega{
		ChainID: chain, Addr: addr, BlockDate: blockDate, Moniker: addr + "-mon",
		ParticipatedCount: participatedCount, MissedCount: 0, TxContributionCount: 0,
		TotalBlocks: int(lastHeight - firstHeight + 1), FirstBlockHeight: firstHeight, LastBlockHeight: lastHeight,
	}
	require.NoError(t, db.Create(&row).Error)
}

func TestCleanupTrailingGhostParticipations(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test-trailing-ghost"
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// g1departed: really participated up to height 100, then kept getting
	// phantom participated=false rows up to height 110 (the bug this
	// cleanup targets). It is NOT part of the current live valset.
	seedDP(t, db, chain, "g1departed", 99, true, day)
	seedDP(t, db, chain, "g1departed", 100, true, day)
	seedDP(t, db, chain, "g1departed", 105, false, day)
	seedDP(t, db, chain, "g1departed", 110, false, day)

	// g1bonded: still in the current live valset, going through a long
	// real downtime that looks identical to g1departed's tail. Must be
	// left completely untouched because it IS in currentAddrs.
	seedDP(t, db, chain, "g1bonded", 99, true, day)
	seedDP(t, db, chain, "g1bonded", 100, true, day)
	seedDP(t, db, chain, "g1bonded", 105, false, day)
	seedDP(t, db, chain, "g1bonded", 110, false, day)

	currentAddrs := []string{"g1bonded"}
	require.NoError(t, database.CleanupTrailingGhostParticipations(context.Background(), db, chain, currentAddrs))

	var departedRows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1departed").
		Order("block_height").Find(&departedRows).Error)
	require.Len(t, departedRows, 2, "only the two participated=true rows should survive for the departed address")
	require.Equal(t, int64(99), departedRows[0].BlockHeight)
	require.Equal(t, int64(100), departedRows[1].BlockHeight)

	var bondedRows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1bonded").
		Order("block_height").Find(&bondedRows).Error)
	require.Len(t, bondedRows, 4, "still-bonded address must be untouched even though its tail looks identical")
}

func TestCleanupTrailingGhostParticipations_EmptyCurrentAddrsIsNoop(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test-trailing-ghost-empty-guard"
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedDP(t, db, chain, "g1anything", 99, true, day)
	seedDP(t, db, chain, "g1anything", 100, false, day)

	// An empty currentAddrs must be treated as "valset unknown" and be a
	// deliberate no-op, not "everyone is departed" (which would otherwise
	// vacuously match every address via addr <> ALL(empty array) = true).
	require.NoError(t, database.CleanupTrailingGhostParticipations(context.Background(), db, chain, nil))

	var rows []database.DailyParticipation
	require.NoError(t, db.Where("chain_id = ?", chain).Find(&rows).Error)
	require.Len(t, rows, 2)
}

func TestCleanupTrailingGhostParticipations_AgregaTable(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chain := "test-trailing-ghost-agrega"
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Establish last_true=100 for both addresses via a raw participated=true row.
	seedDP(t, db, chain, "g1departed", 100, true, day)
	seedDP(t, db, chain, "g1bonded", 100, true, day)

	// Aggregate row entirely BEFORE last_true — must survive for both addresses.
	seedAgrega(t, db, chain, "g1departed", "2025-12-31", 5, 90, 99)
	seedAgrega(t, db, chain, "g1bonded", "2025-12-31", 5, 90, 99)

	// Aggregate row entirely AFTER last_true — phantom tail for g1departed (must be
	// deleted), identical-looking legitimate row for g1bonded (must survive because
	// it's in currentAddrs).
	seedAgrega(t, db, chain, "g1departed", "2026-01-02", 0, 105, 110)
	seedAgrega(t, db, chain, "g1bonded", "2026-01-02", 0, 105, 110)

	currentAddrs := []string{"g1bonded"}
	require.NoError(t, database.CleanupTrailingGhostParticipations(context.Background(), db, chain, currentAddrs))

	var departedAgregas []database.DailyParticipationAgrega
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1departed").
		Order("block_date").Find(&departedAgregas).Error)
	require.Len(t, departedAgregas, 1, "only the pre-last_true aggregate day should survive for the departed address")
	require.Equal(t, "2025-12-31", departedAgregas[0].BlockDate)

	var bondedAgregas []database.DailyParticipationAgrega
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chain, "g1bonded").
		Order("block_date").Find(&bondedAgregas).Error)
	require.Len(t, bondedAgregas, 2, "still-bonded address's aggregate rows must be untouched")
}
