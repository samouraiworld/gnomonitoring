package database_test

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildChainValidatorReport_ExcludesDepartedValidator(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// g1departed: participation/alert history this year, but no addr_monikers
	// row at all (left the valset — same fixture shape as
	// TestGetValidatorReportHandlerExcludesDepartedValidators in internal/api).
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1departed", Moniker: "departed-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AlertLog{
		ChainID: "test12", Addr: "g1departed", Level: "CRITICAL",
		StartHeight: 100, EndHeight: 130, Moniker: "departed-mon", SentAt: now,
	})

	// g1active: currently in the valset (voting_power > 0).
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1active", Moniker: "active-mon",
		BlockHeight: 2, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{
		ChainID: "test12", Addr: "g1active", Moniker: "active-mon", VotingPower: 5,
	})

	ctx, err := database.LoadValidatorReportContext(db, "test12")
	require.NoError(t, err)
	entries, err := database.BuildChainValidatorReport(db, ctx, "test12", "current_year", "")
	require.NoError(t, err)

	for _, e := range entries {
		assert.NotEqual(t, "g1departed", e.Addr, "departed validator must be excluded")
	}
	found := false
	for _, e := range entries {
		if e.Addr == "g1active" {
			found = true
		}
	}
	assert.True(t, found, "active validator must still be present")
}

func TestBuildChainValidatorReport_RosterMemberWithNoDataScoresZeroCritical(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// g1new: in the valset (has a VP row) but no participation/alert rows at
	// all for this period — per the documented score model this must score
	// 0/Critical, not be silently omitted.
	db.Create(&database.AddrMoniker{
		ChainID: "test12", Addr: "g1new", Moniker: "new-mon", VotingPower: 3,
	})

	ctx, err := database.LoadValidatorReportContext(db, "test12")
	require.NoError(t, err)
	entries, err := database.BuildChainValidatorReport(db, ctx, "test12", "last_24h", "")
	require.NoError(t, err)

	require.Len(t, entries, 1)
	assert.Equal(t, "g1new", entries[0].Addr)
	assert.Equal(t, 0, entries[0].Score)
	assert.Equal(t, score.TierCritical, entries[0].Tier)
	// g1new's real voting power (3, from its addr_monikers row) must still be
	// reported even though it never entered the participation/alert merge —
	// regression test for the zero-VP bug on zero-history valset members.
	assert.Equal(t, int64(3), entries[0].VotingPower)
	assert.Equal(t, int64(3), entries[0].SumVotingPower)
}

func TestBuildChainValidatorReport_AddrFilter(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1a", Moniker: "a", BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1a", Moniker: "a", VotingPower: 1})
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1b", Moniker: "b", BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1b", Moniker: "b", VotingPower: 1})

	ctx, err := database.LoadValidatorReportContext(db, "test12")
	require.NoError(t, err)
	entries, err := database.BuildChainValidatorReport(db, ctx, "test12", "current_year", "g1a")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "g1a", entries[0].Addr)
}
