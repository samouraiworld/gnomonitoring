package database_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrationsApplyChainIDColumns verifies that chain_id columns are added
func TestMigrationsApplyChainIDColumns(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Verify chain_id exists in daily_participations
	var result int
	err := db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('daily_participations')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in daily_participations")

	// Verify chain_id exists in alert_logs
	err = db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('alert_logs')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in alert_logs")

	// Verify chain_id exists in addr_monikers
	err = db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('addr_monikers')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in addr_monikers")

	// Verify chain_id exists in govdaos
	err = db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('govdaos')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in govdaos")

	// Verify chain_id exists in telegram_validator_subs
	err = db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('telegram_validator_subs')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in telegram_validator_subs")

	// Verify chain_id exists in telegram_hour_reports
	err = db.Raw(`
		SELECT COUNT(*) FROM pragma_table_info('telegram_hour_reports')
		WHERE name='chain_id'
	`).Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result, "chain_id column should exist in telegram_hour_reports")
}

// TestMigrationsIndexesCreated verifies that compound indexes are created
func TestMigrationsIndexesCreated(t *testing.T) {
	db := testoutils.NewTestDB(t)

	expectedIndexes := []string{
		"idx_dp_chain_block_height",
		"idx_dp_chain_addr",
		"idx_dp_chain_date",
		"idx_dp_chain_addr_participated",
		"idx_al_chain_addr",
		"idx_tvs_chain_addr_chatid",
	}

	for _, indexName := range expectedIndexes {
		var result int
		err := db.Raw(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?
		`, indexName).Scan(&result).Error
		require.NoError(t, err)
		assert.Equal(t, 1, result, "index %s should exist", indexName)
	}
}

// TestInsertWithChainID verifies that records can be inserted with chain_id
func TestInsertWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert DailyParticipation with chain_id
	dp := database.DailyParticipation{
		ChainID:        "test12",
		BlockHeight:    100,
		Addr:           "g1testaddr",
		Participated:   true,
		TxContribution: false,
		Date:           time.Now(),
	}

	err := db.Create(&dp).Error
	assert.NoError(t, err)

	// Verify record was inserted
	var retrieved database.DailyParticipation
	err = db.Where("chain_id = ? AND addr = ?", "test12", "g1testaddr").
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
	assert.Equal(t, int64(100), retrieved.BlockHeight)
}

// TestQueryWithChainIDFilter verifies that chain_id filtering works
func TestQueryWithChainIDFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert records for different chains
	dp1 := database.DailyParticipation{
		ChainID:        "test12",
		BlockHeight:    100,
		Addr:           "g1addr1",
		Participated:   true,
		TxContribution: false,
		Date:           time.Now(),
	}
	dp2 := database.DailyParticipation{
		ChainID:        "gnoland1",
		BlockHeight:    100,
		Addr:           "g1addr1",
		Participated:   false,
		TxContribution: true,
		Date:           time.Now(),
	}

	db.Create(&dp1)
	db.Create(&dp2)

	// Query only test12 chain
	var results []database.DailyParticipation
	err := db.Where("chain_id = ?", "test12").Find(&results).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, len(results))
	assert.Equal(t, "test12", results[0].ChainID)
	assert.True(t, results[0].Participated)

	// Query only gnoland1 chain
	results = []database.DailyParticipation{}
	err = db.Where("chain_id = ?", "gnoland1").Find(&results).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, len(results))
	assert.Equal(t, "gnoland1", results[0].ChainID)
	assert.False(t, results[0].Participated)
}

// TestUniqueConstraintWithChainID verifies compound unique constraints
func TestUniqueConstraintWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert first record
	dp1 := database.DailyParticipation{
		ChainID:        "test12",
		BlockHeight:    100,
		Addr:           "g1addr1",
		Participated:   true,
		TxContribution: false,
		Date:           time.Now(),
	}
	err := db.Create(&dp1).Error
	assert.NoError(t, err)

	// Try to insert duplicate (same chain_id, block_height, addr)
	// This should fail
	dp2 := database.DailyParticipation{
		ChainID:        "test12",
		BlockHeight:    100,
		Addr:           "g1addr1",
		Participated:   false,
		TxContribution: false,
		Date:           time.Now(),
	}
	err = db.Create(&dp2).Error
	assert.Error(t, err, "should fail due to unique constraint")

	// But same addr/block with different chain should succeed
	dp3 := database.DailyParticipation{
		ChainID:        "gnoland1",
		BlockHeight:    100,
		Addr:           "g1addr1",
		Participated:   true,
		TxContribution: false,
		Date:           time.Now(),
	}
	err = db.Create(&dp3).Error
	assert.NoError(t, err)
}

// TestAlertLogWithChainID verifies AlertLog works with chain_id
func TestAlertLogWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	alert := database.AlertLog{
		ChainID:     "test12",
		Addr:        "g1validator1",
		Moniker:     "Validator1",
		Level:       "WARNING",
		StartHeight: 100,
		EndHeight:   110,
		Skipped:     false,
		Msg:         "Test alert",
	}

	err := db.Create(&alert).Error
	assert.NoError(t, err)

	// Verify retrieval
	var retrieved database.AlertLog
	err = db.Where("chain_id = ? AND addr = ?", "test12", "g1validator1").
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
	assert.Equal(t, "WARNING", retrieved.Level)
}

// TestAddrMonikerWithChainID verifies AddrMoniker works with chain_id
func TestAddrMonikerWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	moniker := database.AddrMoniker{
		ChainID: "test12",
		Addr:    "g1addr1",
		Moniker: "TestValidator",
	}

	err := db.Create(&moniker).Error
	assert.NoError(t, err)

	// Verify retrieval
	var retrieved database.AddrMoniker
	err = db.Where("chain_id = ? AND addr = ?", "test12", "g1addr1").
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
	assert.Equal(t, "TestValidator", retrieved.Moniker)
}

// TestAddrMonikerUniquenessPerChain verifies compound unique constraint
func TestAddrMonikerUniquenessPerChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Insert moniker for test12
	m1 := database.AddrMoniker{
		ChainID: "test12",
		Addr:    "g1addr1",
		Moniker: "Moniker1",
	}
	err := db.Create(&m1).Error
	assert.NoError(t, err)

	// Try to insert duplicate for same chain (should fail)
	m2 := database.AddrMoniker{
		ChainID: "test12",
		Addr:    "g1addr1",
		Moniker: "Moniker2",
	}
	err = db.Create(&m2).Error
	assert.Error(t, err, "should fail due to unique constraint")

	// But same addr for different chain should succeed
	m3 := database.AddrMoniker{
		ChainID: "gnoland1",
		Addr:    "g1addr1",
		Moniker: "Moniker3",
	}
	err = db.Create(&m3).Error
	assert.NoError(t, err)

	// Verify both exist with different monikers
	var retrieved []database.AddrMoniker
	err = db.Where("addr = ?", "g1addr1").Find(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, 2, len(retrieved))
}

// TestGovdaoWithChainID verifies Govdao model works with chain_id
func TestGovdaoWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	proposal := database.Govdao{
		Id:      1,
		ChainID: "test12",
		Url:     "https://example.com/proposal/1",
		Title:   "Test Proposal",
		Tx:      "TX123",
		Status:  "active",
	}

	err := db.Create(&proposal).Error
	assert.NoError(t, err)

	// Verify retrieval
	var retrieved database.Govdao
	err = db.Where("chain_id = ? AND id = ?", "test12", 1).
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
	assert.Equal(t, "Test Proposal", retrieved.Title)
}

// TestTelegramValidatorSubWithChainID verifies compound index works
func TestTelegramValidatorSubWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	sub := database.TelegramValidatorSub{
		ChatID:  12345,
		ChainID: "test12",
		Addr:    "g1validator",
		Moniker: "Validator1",
		Activate: true,
	}

	err := db.Create(&sub).Error
	assert.NoError(t, err)

	// Verify retrieval by compound filter
	var retrieved database.TelegramValidatorSub
	err = db.Where("chat_id = ? AND chain_id = ? AND addr = ?", 12345, "test12", "g1validator").
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
}

// TestTelegramHourReportWithChainID verifies composite primary key
func TestTelegramHourReportWithChainID(t *testing.T) {
	db := testoutils.NewTestDB(t)

	report := database.TelegramHourReport{
		ChatID:            12345,
		ChainID:           "test12",
		DailyReportHour:   9,
		DailyReportMinute: 0,
		Activate:          true,
		Timezone:          "Europe/Paris",
	}

	err := db.Create(&report).Error
	assert.NoError(t, err)

	// Verify retrieval
	var retrieved database.TelegramHourReport
	err = db.Where("chat_id = ? AND chain_id = ?", 12345, "test12").
		First(&retrieved).Error
	assert.NoError(t, err)
	assert.Equal(t, "test12", retrieved.ChainID)
	assert.Equal(t, 9, retrieved.DailyReportHour)

	// Same chat_id but different chain should also succeed
	report2 := database.TelegramHourReport{
		ChatID:            12345,
		ChainID:           "gnoland1",
		DailyReportHour:   10,
		DailyReportMinute: 30,
		Activate:          true,
		Timezone:          "UTC",
	}
	err = db.Create(&report2).Error
	assert.NoError(t, err)

	// Both should exist
	var reports []database.TelegramHourReport
	err = db.Where("chat_id = ?", 12345).Find(&reports).Error
	assert.NoError(t, err)
	assert.Equal(t, 2, len(reports))
}

// TestApplyTelegramChainIDMigration verifies that chain_id is added to telegrams table
func TestApplyTelegramChainIDMigration(t *testing.T) {
	db := testoutils.NewTestDB(t)
	var count int
	err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('telegrams') WHERE name='chain_id'`).
		Scan(&count).Error
	require.NoError(t, err)
	assert.Equal(t, 1, count, "chain_id column should exist in telegrams")
}
