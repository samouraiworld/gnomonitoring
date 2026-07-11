package database_test

import (
	"database/sql"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
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
