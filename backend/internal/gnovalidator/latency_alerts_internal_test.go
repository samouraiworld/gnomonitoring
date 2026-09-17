package gnovalidator

import (
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func fieldValue(t *testing.T, d internal.AlertData, name string) string {
	t.Helper()
	for _, f := range d.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	t.Fatalf("field %q missing from %+v", name, d.Fields)
	return ""
}

func TestBuildLatencyAlertData(t *testing.T) {
	ratio := 0.49
	ev := latencyEval{
		Stat: latencyStat{
			Addr: "g1slow", Moniker: "samourai-crew-validator-1", Samples: 1043,
			P50: 67, P90: 120, LateRatio: &ratio, MinHeight: 112000, MaxHeight: 113043,
		},
		PeerMedianP50: 8,
		Verdict:       verdictTrigger,
	}

	d := buildLatencyAlertData("gnoland1", ev, 60)

	if d.Level != internal.AlertLatency {
		t.Errorf("Level = %q, want LATENCY", d.Level)
	}
	if d.Title != "LATENCY — signing late for quorum" {
		t.Errorf("Title = %q", d.Title)
	}
	if len(d.Mentions) != 0 {
		t.Errorf("Mentions = %v, want none on a latency alert", d.Mentions)
	}
	if v := fieldValue(t, d, "validator"); v != "samourai-crew-validator-1 (g1slow)" {
		t.Errorf("validator = %q", v)
	}
	if v := fieldValue(t, d, "precommit lag (60m)"); v != "p50 67 ms / p90 120 ms (peers median p50: 8 ms)" {
		t.Errorf("lag = %q", v)
	}
	if v := fieldValue(t, d, "late for quorum"); v != "49% of 1043 signed blocks" {
		t.Errorf("late for quorum = %q", v)
	}
	if v := fieldValue(t, d, "blocks"); v != "112000 -> 113043" {
		t.Errorf("blocks = %q", v)
	}
	note := fieldValue(t, d, "note")
	for _, want := range []string{"No blocks missed", "flush_throttle_timeout", "peer_gossip_sleep_duration", "timeout_commit", "NTP"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
}

func TestBuildLatencyAlertData_NoQuorumSamples(t *testing.T) {
	ev := latencyEval{
		Stat:          latencyStat{Addr: "g1a", Moniker: "A", Samples: 400, P50: 80, P90: 90, MinHeight: 1, MaxHeight: 2},
		PeerMedianP50: 5,
	}

	d := buildLatencyAlertData("dev", ev, 60)

	for _, f := range d.Fields {
		if f.Name == "late for quorum" {
			t.Fatal("the late-for-quorum field must be omitted when the ratio is unknown")
		}
	}
}

func TestBuildLatencyResolvedData(t *testing.T) {
	ev := latencyEval{
		Stat:          latencyStat{Addr: "g1a", Moniker: "A", Samples: 900, P50: 9, P90: 12, MinHeight: 5, MaxHeight: 9},
		PeerMedianP50: 8,
	}

	d := buildLatencyResolvedData("gnoland1", ev, 60)

	if d.Level != internal.AlertResolved {
		t.Errorf("Level = %q, want RESOLVED", d.Level)
	}
	if d.Title != "LATENCY RESOLVED" {
		t.Errorf("Title = %q", d.Title)
	}
	if v := fieldValue(t, d, "precommit lag (60m)"); v != "p50 9 ms / p90 12 ms (peers median p50: 8 ms)" {
		t.Errorf("lag = %q", v)
	}
}

// seedLatencyRows inserts `n` signed blocks with the given lag for one
// validator of a chain, recent enough to fall inside the alert window.
func seedLatencyRows(t *testing.T, db *gorm.DB, chainID, addr string, lagMs int64, n int, startHeight int64) {
	t.Helper()
	rows := make([]database.DailyParticipation, 0, n)
	late := lagMs > 0
	for i := 0; i < n; i++ {
		lag := lagMs
		l := late
		rows = append(rows, database.DailyParticipation{
			ChainID: chainID, Addr: addr, Moniker: addr,
			BlockHeight:  startHeight + int64(i),
			Date:         time.Now().UTC().Add(-time.Duration(n-i) * time.Second),
			Participated: true, PrecommitLagMs: &lag, LateForQuorum: &l,
		})
	}
	require.NoError(t, db.Create(&rows).Error)
}

// captureLatencyDispatch swaps the dispatch seam for the duration of the test
// and returns the slice the jobs land in.
func captureLatencyDispatch(t *testing.T) *[]latencyJob {
	t.Helper()
	var jobs []latencyJob
	setLatencyDispatch(func(j latencyJob) { jobs = append(jobs, j) })
	t.Cleanup(func() { setLatencyDispatch(defaultLatencyDispatch) })
	return &jobs
}

func latencyTestThresholds() Thresholds {
	return Thresholds{
		LatencyAlertEnabled: true, LatencyAlertCheckMinutes: 1, LatencyAlertWindowMinutes: 60,
		LatencyAlertMinLagMs: 50, LatencyAlertPeerFactor: 3, LatencyAlertMinSamples: 10,
		LatencyAlertResendHours: 24,
	}
}

// seedLatencyChain seeds one slow validator and three fast peers, and makes
// all four the chain's current valset.
func seedLatencyChain(t *testing.T, db *gorm.DB, chainID string) {
	t.Helper()
	seedLatencyRows(t, db, chainID, "g1slow", 150, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1a", 5, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1b", 5, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1c", 5, 20, 1000)
	ReplaceMonikerMap(chainID, map[string]string{"g1slow": "Slow", "g1a": "A", "g1b": "B", "g1c": "C"})
	t.Cleanup(func() { ReplaceMonikerMap(chainID, map[string]string{}) })
}

func TestRunLatencyAlertCycle_Triggers(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-trigger"
	seedLatencyChain(t, db, chainID)
	jobs := captureLatencyDispatch(t)

	runLatencyAlertCycle(db, chainID, latencyTestThresholds())

	require.Len(t, *jobs, 1)
	assert.Equal(t, "LATENCY", (*jobs)[0].level)
	assert.Equal(t, "g1slow", (*jobs)[0].eval.Stat.Addr)
}

func TestRunLatencyAlertCycle_NoResendWhileActive(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-active"
	seedLatencyChain(t, db, chainID)
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1slow", "Slow", "LATENCY", 1000, 1019, true, time.Now().UTC().Add(-time.Hour), ""))
	jobs := captureLatencyDispatch(t)

	runLatencyAlertCycle(db, chainID, latencyTestThresholds())

	assert.Empty(t, *jobs, "an active alert must not be re-sent inside the resend window")
}

func TestRunLatencyAlertCycle_ResendsAfterWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-resend"
	seedLatencyChain(t, db, chainID)
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1slow", "Slow", "LATENCY", 1000, 1019, true, time.Now().UTC().Add(-25*time.Hour), ""))
	jobs := captureLatencyDispatch(t)

	runLatencyAlertCycle(db, chainID, latencyTestThresholds())

	require.Len(t, *jobs, 1)
	assert.Equal(t, "LATENCY", (*jobs)[0].level)
}

func TestRunLatencyAlertCycle_Resolves(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-resolve"
	seedLatencyRows(t, db, chainID, "g1slow", 5, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1a", 5, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1b", 5, 20, 1000)
	seedLatencyRows(t, db, chainID, "g1c", 5, 20, 1000)
	ReplaceMonikerMap(chainID, map[string]string{"g1slow": "Slow", "g1a": "A", "g1b": "B", "g1c": "C"})
	t.Cleanup(func() { ReplaceMonikerMap(chainID, map[string]string{}) })
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1slow", "Slow", "LATENCY", 1000, 1019, true, time.Now().UTC().Add(-time.Hour), ""))
	jobs := captureLatencyDispatch(t)

	runLatencyAlertCycle(db, chainID, latencyTestThresholds())

	require.Len(t, *jobs, 1)
	assert.Equal(t, "LATENCY_RESOLVED", (*jobs)[0].level)
}

func TestRunLatencyAlertCycle_SilentResolveWhenOutOfValset(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-left"
	seedLatencyChain(t, db, chainID)
	// g1gone has an open alert but is not in the valset and has no rows.
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1gone", "Gone", "LATENCY", 900, 950, true, time.Now().UTC().Add(-2*time.Hour), ""))
	jobs := captureLatencyDispatch(t)

	runLatencyAlertCycle(db, chainID, latencyTestThresholds())

	for _, j := range *jobs {
		if j.eval.Stat.Addr == "g1gone" {
			t.Fatal("a validator that left the valset must be resolved silently, with no dispatch")
		}
	}
	states, err := database.GetLatencyAlertStates(db, chainID)
	require.NoError(t, err)
	assert.Equal(t, "LATENCY_RESOLVED", states["g1gone"].Level)
}

func TestRunLatencyAlertCycle_DisabledDoesNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "lat-cycle-disabled"
	seedLatencyChain(t, db, chainID)
	jobs := captureLatencyDispatch(t)

	th := latencyTestThresholds()
	th.LatencyAlertEnabled = false
	runLatencyAlertCycle(db, chainID, th)

	assert.Empty(t, *jobs)
}
