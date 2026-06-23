package testoutils

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// testDSN returns the base connection string for the test Postgres instance.
// Override via TEST_DATABASE_DSN for local dev against a non-default Postgres.
func testDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		return dsn
	}
	return "host=localhost port=5432 user=gnomonitoring password=gnomonitoring dbname=gnomonitoring_test sslmode=disable"
}

// sanitizeSchema keeps only characters that are safe in an unquoted-style
// Postgres identifier so that %q quoting is always valid.
func sanitizeSchema(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// NewTestDB creates an isolated Postgres schema for the test, runs InitDB's
// migrations against it, seeds fake data, and drops the schema on cleanup.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	schema := "test_" + sanitizeSchema(strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")))
	// Postgres identifiers are capped at 63 bytes; keep schema names safe.
	if len(schema) > 63 {
		schema = schema[:63]
	}

	dsn := testDSN() + fmt.Sprintf(" search_path=%s", schema)

	setup, err := gorm.Open(postgres.Open(testDSN()), &gorm.Config{})
	require.NoError(t, err)
	sqlSetup, err := setup.DB()
	require.NoError(t, err)
	_, err = sqlSetup.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	require.NoError(t, err)
	_, err = sqlSetup.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema))
	require.NoError(t, err)
	require.NoError(t, sqlSetup.Close())

	t.Cleanup(func() {
		cleanup, err := gorm.Open(postgres.Open(testDSN()), &gorm.Config{})
		if err != nil {
			return
		}
		sqlCleanup, err := cleanup.DB()
		if err != nil {
			return
		}
		_, _ = sqlCleanup.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
		_ = sqlCleanup.Close()
	})

	db, err := database.InitDB(dsn)
	require.NoError(t, err)

	// Close InitDB's connection pool when the test ends. Registered after the
	// schema-drop cleanup so it runs first (cleanups are LIFO): the pool is
	// closed before the schema is dropped, avoiding both a connection leak
	// (InitDB keeps up to 5 idle conns) and any drop-vs-open-connection
	// contention on the test schema.
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	FakeData(t, db)

	return db
}

// TestChainID is the chainID used by FakeData seeds and should be used in
// tests that query seeded rows.
const TestChainID = "betanet"

func FakeData(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Dates anchored to the current month so queries scoped to "current_month"
	// (e.g. GetCurrentPeriodParticipationRate) can find the seeded rows.
	now := time.Now().UTC()
	day1 := time.Date(now.Year(), now.Month(), 1, 18, 14, 6, 0, time.UTC)
	day2 := time.Date(now.Year(), now.Month(), 2, 18, 14, 6, 0, time.UTC)
	day3 := time.Date(now.Year(), now.Month(), 3, 18, 14, 6, 0, time.UTC)

	// Insert
	rows := []database.DailyParticipation{
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 50, Date: day1, Participated: true, TxContribution: false},
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 51, Date: day2, Participated: true, TxContribution: true},
		{ChainID: TestChainID, Addr: "g1abc", BlockHeight: 52, Date: day3, Participated: false, TxContribution: false},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}
}
