package gnovalidator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBuildBFTAlertData(t *testing.T) {
	ev := evaluateBFT(bftWindow([]int64{10, 10, 11}, []int64{0, 100, 100}), bftTestThresholds(), bftTestNow)

	d := buildBFTAlertData("betanet", bftLevelCritical, ev, 100)

	assert.Equal(t, internal.AlertCritical, d.Level)
	assert.Equal(t, "BFT CRITICAL — quorum at risk", d.Title)
	assert.Empty(t, d.Mentions)
	assert.Equal(t, "21/31 (67.7%), 2/3 validators", fieldValue(t, d, "online voting power"))
	assert.Equal(t, "1.1% of total voting power", fieldValue(t, d, "margin above 2/3"))
	assert.Equal(t, "can lose 0 more validators, largest first (0 with every validator online)", fieldValue(t, d, "tolerable losses"))
	assert.Equal(t, "v0 (10 VP)", fieldValue(t, d, "offline (signed < 50% of last 100 blocks)"))
	assert.Equal(t, "101 -> 200", fieldValue(t, d, "blocks"))

	w := buildBFTAlertData("betanet", bftLevelWarning, ev, 100)
	assert.Equal(t, internal.AlertWarning, w.Level)
	assert.Equal(t, "BFT WARNING — thin quorum margin", w.Title)
}

func TestBuildBFTAlertData_BelowQuorumAndLongOfflineList(t *testing.T) {
	ev := evaluateBFT(bftWindow(repeat(10, 12), append(repeat(100, 1), repeat(0, 11)...)), bftTestThresholds(), bftTestNow)

	d := buildBFTAlertData("betanet", bftLevelCritical, ev, 100)

	assert.Equal(t, "at or below the 2/3 quorum", fieldValue(t, d, "margin above 2/3"))
	off := fieldValue(t, d, "offline (signed < 50% of last 100 blocks)")
	assert.True(t, strings.HasSuffix(off, ", and 1 more"), off)
	assert.Equal(t, bftMaxOfflineListed+1, len(strings.Split(off, ", ")))
}

func TestBuildBFTAlertData_TruncatesLongMonikers(t *testing.T) {
	w := bftWindow(repeat(10, 4), []int64{100, 100, 100, 0})
	w.Entries[3].Moniker = strings.Repeat("x", 500)
	ev := evaluateBFT(w, bftTestThresholds(), bftTestNow)

	off := fieldValue(t, buildBFTAlertData("betanet", bftLevelCritical, ev, 100), "offline (signed < 50% of last 100 blocks)")

	assert.Equal(t, strings.Repeat("x", bftMaxNameRunes-1)+"… (10 VP)", off)
}

func TestBuildBFTResolvedData(t *testing.T) {
	ev := evaluateBFT(bftWindow(repeat(10, 4), repeat(100, 4)), bftTestThresholds(), bftTestNow)

	d := buildBFTResolvedData("betanet", ev)

	assert.Equal(t, internal.AlertResolved, d.Level)
	assert.Equal(t, "BFT RESOLVED — quorum margin restored", d.Title)
	assert.Equal(t, "can lose 1 more validator, largest first (1 with every validator online)", fieldValue(t, d, "tolerable losses"))
}

// seedBFTChain makes vps the chain's voting-power snapshot and writes the
// last 100 blocks, where validator i signed signed[i] of them (the most
// recent ones unsigned).
func seedBFTChain(t *testing.T, db *gorm.DB, chainID string, vps []int64, signed []int64) {
	t.Helper()
	for i, vp := range vps {
		addr := bftWindow(vps, signed).Entries[i].Addr
		require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chainID, Addr: addr, Moniker: addr, VotingPower: vp}).Error)
		rows := make([]database.DailyParticipation, 0, 100)
		for h := int64(101); h <= 200; h++ {
			rows = append(rows, database.DailyParticipation{
				ChainID: chainID, Addr: addr, Moniker: addr, BlockHeight: h,
				Date: time.Now().UTC(), Participated: h-100 <= signed[i],
			})
		}
		require.NoError(t, db.Create(&rows).Error)
	}
}

func captureBFTDispatch(t *testing.T) *[]bftJob {
	t.Helper()
	var jobs []bftJob
	setBFTDispatch(func(j bftJob) { jobs = append(jobs, j) })
	t.Cleanup(func() { setBFTDispatch(defaultBFTDispatch) })
	return &jobs
}

func TestRunBFTAlertCycle_TriggersAndSetsGauge(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-trigger"
	seedBFTChain(t, db, chainID, []int64{10, 10, 11}, []int64{0, 100, 100})
	jobs := captureBFTDispatch(t)

	runBFTAlertCycle(context.Background(), db, chainID, bftTestThresholds())

	require.Len(t, *jobs, 1)
	assert.Equal(t, bftLevelCritical, (*jobs)[0].level)
	assert.Equal(t, 100, (*jobs)[0].windowBlocks)
	assert.InDelta(t, 1.1, testutil.ToFloat64(ChainBFTMarginPercent.WithLabelValues(chainID)), 0.05)
}

func TestRunBFTAlertCycle_NoResendWhileActive(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-active"
	seedBFTChain(t, db, chainID, []int64{10, 10, 11}, []int64{0, 100, 100})
	require.NoError(t, database.InsertAlertlog(db, chainID, bftAlertAddr, bftAlertAddr, bftLevelCritical, 101, 200, false, time.Now().UTC().Add(-time.Hour), ""))
	jobs := captureBFTDispatch(t)

	runBFTAlertCycle(context.Background(), db, chainID, bftTestThresholds())

	assert.Empty(t, *jobs, "an open alert of the same level must not be re-sent inside the resend window")
}

func TestRunBFTAlertCycle_ResolvesAfterTwoHealthyChecks(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-resolve"
	seedBFTChain(t, db, chainID, []int64{10, 10, 11}, []int64{100, 100, 100})
	require.NoError(t, database.InsertAlertlog(db, chainID, bftAlertAddr, bftAlertAddr, bftLevelCritical, 101, 200, false, time.Now().UTC().Add(-time.Hour), ""))
	t.Cleanup(func() {
		bftOKStreaksMu.Lock()
		delete(bftOKStreaks, chainID)
		bftOKStreaksMu.Unlock()
	})
	jobs := captureBFTDispatch(t)

	runBFTAlertCycle(context.Background(), db, chainID, bftTestThresholds())
	assert.Empty(t, *jobs, "one healthy check is not enough to resolve")

	runBFTAlertCycle(context.Background(), db, chainID, bftTestThresholds())
	require.Len(t, *jobs, 1)
	assert.Equal(t, bftLevelResolved, (*jobs)[0].level)
}

func TestRunBFTAlertCycle_NoSnapshotDoesNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-nosnap"
	seedBFTChain(t, db, chainID, []int64{0, 0, 0}, []int64{0, 0, 0})
	jobs := captureBFTDispatch(t)
	// A margin left over from an earlier check must not outlive the snapshot.
	ChainBFTMarginPercent.WithLabelValues(chainID).Set(20)

	runBFTAlertCycle(context.Background(), db, chainID, bftTestThresholds())

	assert.Empty(t, *jobs)
	assert.False(t, ChainBFTMarginPercent.DeleteLabelValues(chainID), "the gauge must be removed when nothing can be evaluated")
}

func TestRunBFTAlertCycle_CancelledContextDoesNotDispatch(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-cancelled"
	seedBFTChain(t, db, chainID, []int64{10, 10, 11}, []int64{0, 100, 100})
	jobs := captureBFTDispatch(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runBFTAlertCycle(ctx, db, chainID, bftTestThresholds())

	assert.Empty(t, *jobs)
}

func TestRunBFTAlertCycle_DisabledDoesNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-cycle-disabled"
	seedBFTChain(t, db, chainID, []int64{10, 10, 11}, []int64{0, 100, 100})
	jobs := captureBFTDispatch(t)

	th := bftTestThresholds()
	th.BFTAlertEnabled = false
	runBFTAlertCycle(context.Background(), db, chainID, th)

	assert.Empty(t, *jobs)
}

func TestDefaultBFTDispatch_WritesAlertLog(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "bft-dispatch"
	ev := evaluateBFT(bftWindow([]int64{10, 10, 11}, []int64{0, 100, 100}), bftTestThresholds(), bftTestNow)

	defaultBFTDispatch(bftJob{db: db, chainID: chainID, level: bftLevelCritical, eval: ev, windowBlocks: 100})

	st, err := database.GetBFTAlertState(db, chainID)
	require.NoError(t, err)
	assert.Equal(t, bftLevelCritical, st.Level)
	var row database.AlertLog
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chainID, bftAlertAddr).First(&row).Error)
	assert.False(t, row.Skipped, "BFT rows must never look like a dispatched validator incident")
	assert.Equal(t, "online_vp=21/31 margin=1.1% tolerable=0/0 offline=1", row.Msg)
}
