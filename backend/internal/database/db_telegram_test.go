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

// TestGetTelegramValidatorSub_ChainFilter inserts two subscriptions for the
// same address on two different chains and verifies that querying by chainID
// returns only the matching row.
func TestGetTelegramValidatorSub_ChainFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chatID int64 = 1001
	const addr = "g1val_same_addr"

	err := database.InsertTelegramValidatorSub(db, chatID, "chainA", "MonikerA", addr)
	require.NoError(t, err)

	err = database.InsertTelegramValidatorSub(db, chatID, "chainB", "MonikerB", addr)
	require.NoError(t, err)

	// Query chainA only.
	subsA, err := database.GetTelegramValidatorSub(db, chatID, "chainA", false)
	require.NoError(t, err)
	require.Len(t, subsA, 1, "should return exactly one subscription for chainA")
	assert.Equal(t, "chainA", subsA[0].ChainID)
	assert.Equal(t, addr, subsA[0].Addr)

	// Query chainB only.
	subsB, err := database.GetTelegramValidatorSub(db, chatID, "chainB", false)
	require.NoError(t, err)
	require.Len(t, subsB, 1, "should return exactly one subscription for chainB")
	assert.Equal(t, "chainB", subsB[0].ChainID)
	assert.Equal(t, addr, subsB[0].Addr)

	// A chain that was never subscribed returns an empty slice.
	subsC, err := database.GetTelegramValidatorSub(db, chatID, "chainC", false)
	require.NoError(t, err)
	assert.Empty(t, subsC, "unknown chain should return no subscriptions")
}

// TestUpdateTelegramValidatorSubStatus_ChainScope subscribes to the same
// address on two different chains and then unsubscribes on chainA only.
// It verifies that chainB remains active and chainA is deactivated.
func TestUpdateTelegramValidatorSubStatus_ChainScope(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chatID int64 = 1002
	const addr = "g1val_scope"

	// Subscribe to both chains.
	err := database.InsertTelegramValidatorSub(db, chatID, "chainA", "MonikerA", addr)
	require.NoError(t, err)
	err = database.InsertTelegramValidatorSub(db, chatID, "chainB", "MonikerB", addr)
	require.NoError(t, err)

	// Verify two distinct rows exist.
	subsA, err := database.GetTelegramValidatorSub(db, chatID, "chainA", true)
	require.NoError(t, err)
	require.Len(t, subsA, 1)

	subsB, err := database.GetTelegramValidatorSub(db, chatID, "chainB", true)
	require.NoError(t, err)
	require.Len(t, subsB, 1)

	// Unsubscribe on chainA.
	err = database.UpdateTelegramValidatorSubStatus(db, chatID, "chainA", addr, "MonikerA", "unsubscribe")
	require.NoError(t, err)

	// chainA should now have no active subscription.
	subsAActive, err := database.GetTelegramValidatorSub(db, chatID, "chainA", true)
	require.NoError(t, err)
	assert.Empty(t, subsAActive, "chainA subscription should be inactive after unsubscribe")

	// chainB must remain active and unaffected.
	subsBActive, err := database.GetTelegramValidatorSub(db, chatID, "chainB", true)
	require.NoError(t, err)
	require.Len(t, subsBActive, 1, "chainB subscription must remain active")
	assert.True(t, subsBActive[0].Activate, "chainB subscription should still be active")
}

// TestGetValidatorStatusList_ChainFilter seeds participation rows on two
// chains and verifies that GetValidatorStatusList returns only the validators
// that belong to the requested chain.
func TestGetValidatorStatusList_ChainFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	const chatID int64 = 1003

	chainAData := []database.DailyParticipation{
		{ChainID: "chainA", Addr: "g1valA1", Moniker: "ValidatorA1", BlockHeight: 100, Date: now, Participated: true},
		{ChainID: "chainA", Addr: "g1valA2", Moniker: "ValidatorA2", BlockHeight: 101, Date: now, Participated: false},
	}
	chainBData := []database.DailyParticipation{
		{ChainID: "chainB", Addr: "g1valB1", Moniker: "ValidatorB1", BlockHeight: 200, Date: now, Participated: true},
	}

	require.NoError(t, db.Create(&chainAData).Error)
	require.NoError(t, db.Create(&chainBData).Error)

	// Query the status list scoped to chainA.
	listA, err := database.GetValidatorStatusList(db, chatID, "chainA")
	require.NoError(t, err)
	require.NotEmpty(t, listA, "chainA should have validators")

	addrSet := make(map[string]struct{}, len(listA))
	for _, v := range listA {
		addrSet[v.Addr] = struct{}{}
	}
	assert.Contains(t, addrSet, "g1valA1", "ValidatorA1 should be in chainA list")
	assert.Contains(t, addrSet, "g1valA2", "ValidatorA2 should be in chainA list")
	assert.NotContains(t, addrSet, "g1valB1", "chainB validator must not appear in chainA list")

	// Query the status list scoped to chainB.
	listB, err := database.GetValidatorStatusList(db, chatID, "chainB")
	require.NoError(t, err)
	require.NotEmpty(t, listB, "chainB should have validators")

	addrSetB := make(map[string]struct{}, len(listB))
	for _, v := range listB {
		addrSetB[v.Addr] = struct{}{}
	}
	assert.Contains(t, addrSetB, "g1valB1", "ValidatorB1 should be in chainB list")
	assert.NotContains(t, addrSetB, "g1valA1", "chainA validator must not appear in chainB list")
	assert.NotContains(t, addrSetB, "g1valA2", "chainA validator must not appear in chainB list")
}

// TestGetAllValidators_ChainFilter verifies that GetAllValidators scopes its
// query to the supplied chainID and does not return validators from other chains.
func TestGetAllValidators_ChainFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	now := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)

	chainXData := []database.DailyParticipation{
		{ChainID: "chainX", Addr: "g1valX1", Moniker: "ValidatorX1", BlockHeight: 300, Date: now, Participated: true},
		{ChainID: "chainX", Addr: "g1valX2", Moniker: "ValidatorX2", BlockHeight: 301, Date: now, Participated: true},
	}
	chainYData := []database.DailyParticipation{
		{ChainID: "chainY", Addr: "g1valY1", Moniker: "ValidatorY1", BlockHeight: 400, Date: now, Participated: true},
	}

	require.NoError(t, db.Create(&chainXData).Error)
	require.NoError(t, db.Create(&chainYData).Error)

	// GetAllValidators for chainX.
	valsX, err := database.GetAllValidators(db, "chainX")
	require.NoError(t, err)
	require.NotEmpty(t, valsX)

	addrX := make(map[string]struct{}, len(valsX))
	for _, v := range valsX {
		addrX[v.Addr] = struct{}{}
	}
	assert.Contains(t, addrX, "g1valX1")
	assert.Contains(t, addrX, "g1valX2")
	assert.NotContains(t, addrX, "g1valY1", "chainY validator must not appear in chainX result")

	// GetAllValidators for chainY.
	valsY, err := database.GetAllValidators(db, "chainY")
	require.NoError(t, err)
	require.NotEmpty(t, valsY)

	addrY := make(map[string]struct{}, len(valsY))
	for _, v := range valsY {
		addrY[v.Addr] = struct{}{}
	}
	assert.Contains(t, addrY, "g1valY1")
	assert.NotContains(t, addrY, "g1valX1", "chainX validator must not appear in chainY result")
	assert.NotContains(t, addrY, "g1valX2", "chainX validator must not appear in chainY result")
}

// TestActivateTelegramReport_ChainScope creates hour-report rows on two chains
// for the same chat, activates the report on chainA only, and verifies that
// chainB is not affected.
func TestActivateTelegramReport_ChainScope(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chatID int64 = 1004

	// InsertChatID creates a TelegramHourReport for the supplied chain.
	_, err := database.InsertChatID(db, chatID, "validator", "chainA")
	require.NoError(t, err)

	// Manually create a second hour-report row for chainB (InsertChatID only
	// creates one row per call — the second chain needs its own call).
	_, err = database.InsertChatID(db, chatID, "validator", "chainB")
	require.NoError(t, err)

	// Both chains start with activate=true (the default from createHourReportTelegram).
	// Deactivate chainA.
	err = database.ActivateTelegramReport(db, false, chatID, "chainA")
	require.NoError(t, err)

	statusA, err := database.GetTelegramReportStatus(db, chatID, "chainA")
	require.NoError(t, err)
	assert.False(t, statusA, "chainA report should be deactivated")

	statusB, err := database.GetTelegramReportStatus(db, chatID, "chainB")
	require.NoError(t, err)
	assert.True(t, statusB, "chainB report should remain active after only chainA was deactivated")

	// Reactivate chainA and verify both are active.
	err = database.ActivateTelegramReport(db, true, chatID, "chainA")
	require.NoError(t, err)

	statusA2, err := database.GetTelegramReportStatus(db, chatID, "chainA")
	require.NoError(t, err)
	assert.True(t, statusA2, "chainA report should be active after re-activation")
}
