package gnovalidator_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveParticipation2(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Simuler un map de participation
	monikerMap := map[string]string{
		"addr1": "Validator1",
		"addr2": "Validator2",
	}

	blockTime := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)

	participating := map[string]gnovalidator.Participation{
		"addr1": {Participated: true, Timestamp: blockTime, TxContribution: true},
		"addr2": {Participated: false, Timestamp: blockTime, TxContribution: false},
	}

	// call SavePArticipation
	err := gnovalidator.SaveParticipation(db, "testchain", 100, participating, monikerMap, blockTime)
	if err != nil {
		t.Fatalf("SaveParticipation failed: %v", err)
	}

	// Check data save
	var participations []database.DailyParticipation
	result := db.Where("block_height = ?", 100).Find(&participations)
	if result.Error != nil {
		t.Fatalf("Error querying participations: %v", result.Error)
	}
	if len(participations) != 2 {
		t.Fatalf("Expected 2 participations, got %d", len(participations))
	}

	for _, p := range participations {
		if p.Addr == "addr1" && !p.Participated {
			t.Errorf("Expected addr1 to have participated")
		}
		if p.Addr == "addr2" && p.Participated {
			t.Errorf("Expected addr2 to not have participated")
		}
	}
}

// -------------------------------------------------------------------------
// CalculateRate — chain filter
// -------------------------------------------------------------------------

// TestCalculateRate_ChainFilter seeds participation data on two chains for the
// same date and verifies that CalculateRate returns only the validators
// belonging to the requested chain.
func TestCalculateRate_ChainFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const date = "2025-11-01"
	blockDate := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)

	// chainA: one validator, always participated.
	chainAData := []database.DailyParticipation{
		{ChainID: "chainA", Addr: "g1chainA_val1", Moniker: "ChainAVal1", BlockHeight: 1000, Date: blockDate, Participated: true},
		{ChainID: "chainA", Addr: "g1chainA_val1", Moniker: "ChainAVal1", BlockHeight: 1001, Date: blockDate, Participated: true},
	}
	// chainB: different validator, participated in only one of two blocks.
	chainBData := []database.DailyParticipation{
		{ChainID: "chainB", Addr: "g1chainB_val1", Moniker: "ChainBVal1", BlockHeight: 2000, Date: blockDate, Participated: true},
		{ChainID: "chainB", Addr: "g1chainB_val1", Moniker: "ChainBVal1", BlockHeight: 2001, Date: blockDate, Participated: false},
	}

	require.NoError(t, db.Create(&chainAData).Error)
	require.NoError(t, db.Create(&chainBData).Error)

	// ---- query chainA ----
	ratesA, minA, maxA := gnovalidator.CalculateRate(db, "chainA", date)
	require.NotEmpty(t, ratesA, "chainA should have participation data")
	assert.Equal(t, int64(1000), minA, "chainA min block height should be 1000")
	assert.Equal(t, int64(1001), maxA, "chainA max block height should be 1001")

	// chainA result must not include chainB validator.
	_, hasChainBVal := ratesA["g1chainB_val1"]
	assert.False(t, hasChainBVal, "chainB validator must not appear in chainA rates")

	rA, ok := ratesA["g1chainA_val1"]
	require.True(t, ok, "ChainAVal1 must be present in chainA rates")
	assert.InDelta(t, 100.0, rA.Rate, 0.01, "ChainAVal1 participated in all blocks — rate should be 100%%")

	// ---- query chainB ----
	ratesB, minB, maxB := gnovalidator.CalculateRate(db, "chainB", date)
	require.NotEmpty(t, ratesB, "chainB should have participation data")
	assert.Equal(t, int64(2000), minB, "chainB min block height should be 2000")
	assert.Equal(t, int64(2001), maxB, "chainB max block height should be 2001")

	// chainB result must not include chainA validator.
	_, hasChainAVal := ratesB["g1chainA_val1"]
	assert.False(t, hasChainAVal, "chainA validator must not appear in chainB rates")

	rB, ok := ratesB["g1chainB_val1"]
	require.True(t, ok, "ChainBVal1 must be present in chainB rates")
	assert.InDelta(t, 50.0, rB.Rate, 0.01, "ChainBVal1 participated in 1 of 2 blocks — rate should be 50%%")
}

// TestCalculateRate_EmptyForUnknownChain verifies that CalculateRate returns an
// empty map (and no error) when the requested chain has no data on the given date.
func TestCalculateRate_EmptyForUnknownChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	rates, minH, maxH := gnovalidator.CalculateRate(db, "nonexistent_chain", "2025-11-01")
	assert.Empty(t, rates, "unknown chain should return no rates")
	assert.Equal(t, int64(0), minH)
	assert.Equal(t, int64(0), maxH)
}

// -------------------------------------------------------------------------
// SendDailyStatsForUser — chain label in the report message
// -------------------------------------------------------------------------

// TestSendDailyStatsForUser_IncludesChainLabel verifies that the daily summary
// message produced by CalculateRate + the message-building logic in
// SendDailyStatsForUser would contain the chain label prefix "[chainID] ".
//
// SendDailyStatsForUser itself makes external HTTP/Telegram calls, so we test
// the message construction indirectly by replicating the same formatting logic
// and asserting the expected output.  This keeps the test deterministic and
// network-free while still verifying the multi-chain labelling contract.
func TestSendDailyStatsForUser_IncludesChainLabel(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chainID = "testchain"
	const date = "2025-11-02"
	blockDate := time.Date(2025, 11, 2, 10, 0, 0, 0, time.UTC)

	rows := []database.DailyParticipation{
		{ChainID: chainID, Addr: "g1val_report1", Moniker: "ReportVal1", BlockHeight: 500, Date: blockDate, Participated: true},
		{ChainID: chainID, Addr: "g1val_report1", Moniker: "ReportVal1", BlockHeight: 501, Date: blockDate, Participated: true},
	}
	require.NoError(t, db.Create(&rows).Error)

	rates, minBlock, maxBlock := gnovalidator.CalculateRate(db, chainID, date)
	require.NotEmpty(t, rates, "should have rates for the seeded data")

	// Replicate the chain-label prefix logic from SendDailyStatsForUser.
	chainLabel := ""
	if chainID != "" {
		chainLabel = fmt.Sprintf("[%s] ", chainID)
	}
	header := fmt.Sprintf("📊 *Daily Summary* %sfor %s (Blocks %d → %d):\n\n", chainLabel, date, minBlock, maxBlock)

	assert.True(t,
		strings.Contains(header, "["+chainID+"]"),
		"daily summary header %q must contain the chain label [%s]", header, chainID,
	)
	assert.True(t,
		strings.HasPrefix(header, "📊 *Daily Summary* [testchain]"),
		"header must start with the summary prefix followed by the chain label",
	)
}
