package gnovalidator_test

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiChain_SaveAndQueryParticipation seeds participation rows for two
// independent chains and verifies that CalculateRate returns isolated results
// per chain. Data for chainA must not appear in chainB results and vice-versa.
func TestMultiChain_SaveAndQueryParticipation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	blockTime := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)
	date := blockTime.Format("2006-01-02")

	// Insert participation for chainA: addr1 participated, addr2 did not.
	chainAMonikers := map[string]string{
		"addr_a1": "ChainA-Validator1",
		"addr_a2": "ChainA-Validator2",
	}
	chainAParticipation := map[string]gnovalidator.Participation{
		"addr_a1": {Participated: true, Timestamp: blockTime, TxContribution: false},
		"addr_a2": {Participated: false, Timestamp: blockTime, TxContribution: false},
	}
	err := gnovalidator.SaveParticipation(db, "chainA", 1000, chainAParticipation, chainAMonikers, blockTime)
	require.NoError(t, err)

	// Insert participation for chainB: completely different addresses and rates.
	chainBMonikers := map[string]string{
		"addr_b1": "ChainB-Validator1",
	}
	chainBParticipation := map[string]gnovalidator.Participation{
		"addr_b1": {Participated: true, Timestamp: blockTime, TxContribution: true},
	}
	err = gnovalidator.SaveParticipation(db, "chainB", 500, chainBParticipation, chainBMonikers, blockTime)
	require.NoError(t, err)

	// CalculateRate for chainA must return exactly the two chainA addresses.
	ratesA, minA, maxA := gnovalidator.CalculateRate(db, "chainA", date)
	require.Len(t, ratesA, 2, "chainA should have exactly 2 validators")
	assert.Equal(t, int64(1000), minA)
	assert.Equal(t, int64(1000), maxA)

	valA1, ok := ratesA["addr_a1"]
	require.True(t, ok, "addr_a1 must be present in chainA results")
	assert.InDelta(t, 100.0, valA1.Rate, 0.01, "addr_a1 participated 1/1 block")

	valA2, ok := ratesA["addr_a2"]
	require.True(t, ok, "addr_a2 must be present in chainA results")
	assert.InDelta(t, 0.0, valA2.Rate, 0.01, "addr_a2 participated 0/1 block")

	// chainB addresses must not leak into chainA.
	_, found := ratesA["addr_b1"]
	assert.False(t, found, "chainB address must not appear in chainA results")

	// CalculateRate for chainB must return only the chainB address.
	ratesB, minB, maxB := gnovalidator.CalculateRate(db, "chainB", date)
	require.Len(t, ratesB, 1, "chainB should have exactly 1 validator")
	assert.Equal(t, int64(500), minB)
	assert.Equal(t, int64(500), maxB)

	valB1, ok := ratesB["addr_b1"]
	require.True(t, ok, "addr_b1 must be present in chainB results")
	assert.InDelta(t, 100.0, valB1.Rate, 0.01, "addr_b1 participated 1/1 block")

	// chainA addresses must not leak into chainB.
	_, found = ratesB["addr_a1"]
	assert.False(t, found, "chainA address must not appear in chainB results")
}

// TestMultiChain_GetLastStoredHeightIsolation inserts blocks at different
// heights for two chains and verifies that GetLastStoredHeight returns the
// correct maximum per chain without cross-contamination.
func TestMultiChain_GetLastStoredHeightIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	blockTime := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)

	monikers := map[string]string{"addr1": "Val1"}
	participation := map[string]gnovalidator.Participation{
		"addr1": {Participated: true, Timestamp: blockTime},
	}

	// chainA: blocks 100 and 200.
	err := gnovalidator.SaveParticipation(db, "chainA", 100, participation, monikers, blockTime)
	require.NoError(t, err)
	err = gnovalidator.SaveParticipation(db, "chainA", 200, participation, monikers, blockTime)
	require.NoError(t, err)

	// chainB: block 50 only.
	err = gnovalidator.SaveParticipation(db, "chainB", 50, participation, monikers, blockTime)
	require.NoError(t, err)

	// GetLastStoredHeight subtracts 1 from MAX(block_height) per implementation.
	heightA, err := gnovalidator.GetLastStoredHeight(db, "chainA")
	require.NoError(t, err)
	assert.Equal(t, int64(199), heightA, "chainA last stored height should be MAX-1 = 199")

	heightB, err := gnovalidator.GetLastStoredHeight(db, "chainB")
	require.NoError(t, err)
	assert.Equal(t, int64(49), heightB, "chainB last stored height should be MAX-1 = 49")

	// A chain with no rows should return 0.
	heightC, err := gnovalidator.GetLastStoredHeight(db, "chainC")
	require.NoError(t, err)
	assert.Equal(t, int64(0), heightC, "unknown chain should return 0")
}

// TestMultiChain_MonikerMapIsolation verifies that SetMoniker and GetMonikerMap
// maintain fully isolated maps per chain. The same address may carry different
// monikers on different chains without interference.
func TestMultiChain_MonikerMapIsolation(t *testing.T) {
	// Use distinct chain IDs to avoid colliding with state from other tests.
	const chainX = "iso-chain-x"
	const chainY = "iso-chain-y"
	const sharedAddr = "g1shared000"

	// Register the same address with a different moniker on each chain.
	gnovalidator.SetMoniker(chainX, sharedAddr, "MonikerOnX")
	gnovalidator.SetMoniker(chainY, sharedAddr, "MonikerOnY")

	mapX := gnovalidator.GetMonikerMap(chainX)
	mapY := gnovalidator.GetMonikerMap(chainY)

	assert.Equal(t, "MonikerOnX", mapX[sharedAddr],
		"chainX should return its own moniker for sharedAddr")
	assert.Equal(t, "MonikerOnY", mapY[sharedAddr],
		"chainY should return its own moniker for sharedAddr")

	// Verify that a mutation on one chain does not affect the other.
	gnovalidator.SetMoniker(chainX, sharedAddr, "MonikerOnX-Updated")

	mapXAfter := gnovalidator.GetMonikerMap(chainX)
	mapYAfter := gnovalidator.GetMonikerMap(chainY)

	assert.Equal(t, "MonikerOnX-Updated", mapXAfter[sharedAddr],
		"chainX moniker should reflect the update")
	assert.Equal(t, "MonikerOnY", mapYAfter[sharedAddr],
		"chainY moniker must not be affected by chainX update")

	// An address set only on chainX must not appear in chainY's map.
	const addrOnlyX = "g1onlyx111"
	gnovalidator.SetMoniker(chainX, addrOnlyX, "OnlyInX")

	mapYFinal := gnovalidator.GetMonikerMap(chainY)
	_, existsInY := mapYFinal[addrOnlyX]
	assert.False(t, existsInY, "address registered only on chainX must not appear in chainY map")
}

// TestMultiChain_AlertLogIsolation inserts AlertLog rows for two chains and
// verifies that GetAlertLog returns only the rows belonging to the requested
// chain.
func TestMultiChain_AlertLogIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)

	sentAt := time.Date(2025, 11, 1, 9, 0, 0, 0, time.UTC)

	// Insert one alert for chainA and one for chainB.
	err := database.InsertAlertlog(db, "chainA", "g1aaaa", "ValidatorAlpha", "CRITICAL", 300, 350, true, sentAt, "chainA alert")
	require.NoError(t, err)

	err = database.InsertAlertlog(db, "chainB", "g1bbbb", "ValidatorBeta", "WARNING", 100, 104, true, sentAt, "chainB alert")
	require.NoError(t, err)

	// Query for chainA using "all_time" period.
	alertsA, err := database.GetAlertLog(db, "chainA", "all_time")
	require.NoError(t, err)
	require.Len(t, alertsA, 1, "GetAlertLog for chainA should return exactly 1 alert")
	assert.Equal(t, "ValidatorAlpha", alertsA[0].Moniker)
	assert.Equal(t, "CRITICAL", alertsA[0].Level)

	// Verify chainB data does not appear in chainA results.
	for _, a := range alertsA {
		assert.NotEqual(t, "ValidatorBeta", a.Moniker,
			"chainB alert must not appear in chainA results")
	}

	// Query for chainB using "all_time" period.
	alertsB, err := database.GetAlertLog(db, "chainB", "all_time")
	require.NoError(t, err)
	require.Len(t, alertsB, 1, "GetAlertLog for chainB should return exactly 1 alert")
	assert.Equal(t, "ValidatorBeta", alertsB[0].Moniker)
	assert.Equal(t, "WARNING", alertsB[0].Level)

	// Verify chainA data does not appear in chainB results.
	for _, a := range alertsB {
		assert.NotEqual(t, "ValidatorAlpha", a.Moniker,
			"chainA alert must not appear in chainB results")
	}
}
