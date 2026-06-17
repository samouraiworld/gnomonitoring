package database_test

import (
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

func TestApplyMultiChainMigrations_SchemaScoped(t *testing.T) {
	db1 := testoutils.NewTestDB(t)
	db2 := testoutils.NewTestDB(t)

	require.NoError(t, database.ApplyMultiChainMigrations(db1))
	require.NoError(t, database.ApplyMultiChainMigrations(db2))
	require.NoError(t, database.ApplyTelegramChainIDMigration(db1))
	require.NoError(t, database.ApplyTelegramChainIDMigration(db2))
}
