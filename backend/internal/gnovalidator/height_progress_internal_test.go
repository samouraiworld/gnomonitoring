package gnovalidator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestClassifyHeightObservation(t *testing.T) {
	cases := []struct {
		name     string
		latest   int64
		lastSeen int64
		want     heightObservation
	}{
		{"first observation", 100, 0, heightAdvanced},
		{"advanced", 101, 100, heightAdvanced},
		{"stalled", 100, 100, heightStalled},
		{"regressed by one", 99, 100, heightRegressed},
		{"regressed a lot", 40, 100, heightRegressed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyHeightObservation(tc.latest, tc.lastSeen))
		})
	}
}

// TestEvaluateRegression covers the persistence tracking that distinguishes a
// transient regression (endpoint fail-over flap, must keep being ignored no
// matter how many times it repeats) from a persistent one (the chain
// genuinely rewound and is now stalled at the lower height, must eventually
// be accepted so the stalled branch — and its CRITICAL alert — can run).
func TestEvaluateRegression(t *testing.T) {
	bound := 20 * time.Second
	t0 := time.Now()

	t.Run("a regression seen repeatedly within bound stays ignored", func(t *testing.T) {
		since := time.Time{}

		accept, next := evaluateRegression(since, t0, bound)
		assert.False(t, accept)
		assert.Equal(t, t0, next, "first regressed observation starts the tracker at 'now'")
		since = next

		accept, next = evaluateRegression(since, t0.Add(5*time.Second), bound)
		assert.False(t, accept)
		assert.Equal(t, t0, next, "regressedSince must not move while repeated observations stay within bound")
		since = next

		accept, next = evaluateRegression(since, t0.Add(bound), bound)
		assert.False(t, accept, "exactly at the bound is not yet past it")
		since = next

		accept, _ = evaluateRegression(since, t0.Add(bound-time.Nanosecond), bound)
		assert.False(t, accept)
	})

	t.Run("a regression is accepted once it exceeds bound", func(t *testing.T) {
		since := t0

		accept, next := evaluateRegression(since, t0.Add(bound+time.Second), bound)
		assert.True(t, accept, "a regression that persisted past the bound must be accepted as the new baseline")
		assert.True(t, next.IsZero(), "the tracker resets once the regression is accepted")
	})

	t.Run("a non-regressed observation in between resets the tracker", func(t *testing.T) {
		// A regression started tracking at t0, but before it could accumulate
		// past bound a non-regressed observation arrived. CollectParticipation
		// resets the tracker to the zero Time in that case rather than calling
		// evaluateRegression, so that is the state a fresh episode starts from.
		since := time.Time{}

		// A fresh regression episode beginning well after t0+bound must NOT
		// be immediately accepted: it gets its own bound window and does not
		// inherit elapsed time from the earlier, unrelated episode.
		accept, next := evaluateRegression(since, t0.Add(bound+time.Second), bound)
		assert.False(t, accept, "a freshly reset tracker must not accept just because a lot of absolute time has passed since an earlier, distinct episode")
		assert.Equal(t, t0.Add(bound+time.Second), next, "the reset episode starts its own clock at the observation time")
	})
}

// --- Fix 3 regression coverage --------------------------------------------
//
// The two tests below drive the real CollectParticipation loop (not just
// the pure classifyHeightObservation/evaluateRegression helpers above) with
// a scripted sequence of polled heights, to verify the actual behavioral
// fix: accepting a persistent regression must not fire an instant false
// "Blockchain stuck" CRITICAL when the new baseline turns out to be a
// healthy, merely-lagging endpoint, while a genuinely halted, rewound node
// must still eventually alert — just one StagnationFirstAlert() window
// later than an instant-fire would have.

// stagnationTestClient scripts a fixed sequence of LatestBlockHeight()
// results (via Status()) for CollectParticipation to poll, then errors on
// every call past the scripted sequence — driving CollectParticipation into
// its RPC-error backoff branch, where it promptly exits once the test
// cancels its context. Block() always returns a minimal, valid block (see
// minimalValidBlock in fake_rpc_client_internal_test.go): only the scripted
// heights matter to these tests, not block content.
type stagnationTestClient struct {
	fakeRPCClient

	mu      sync.Mutex
	heights []int64
	calls   int
}

func newStagnationTestClient(heights []int64) *stagnationTestClient {
	c := &stagnationTestClient{heights: heights}
	c.fakeRPCClient.blockFunc = func(height int64) (*ctypes.ResultBlock, error) {
		return minimalValidBlock(height), nil
	}
	return c
}

func (c *stagnationTestClient) Status() (*ctypes.ResultStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls >= len(c.heights) {
		c.calls++
		return nil, errFakeRPC
	}
	h := c.heights[c.calls]
	c.calls++
	return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{LatestBlockHeight: h}}, nil
}

func (c *stagnationTestClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// setStagnationFirstAlertSecondsForTest lowers the stagnation bound so the
// two 3-second backoff sleeps already hardcoded in CollectParticipation's
// regression path are, on their own, enough to push a regression episode
// past the bound within the test's lifetime — without waiting out the real
// default (20s) threshold. Restores the original value on cleanup.
func setStagnationFirstAlertSecondsForTest(t *testing.T, seconds int) {
	t.Helper()
	thresholdsMu.Lock()
	orig := activeThresholds.StagnationFirstAlertSeconds
	activeThresholds.StagnationFirstAlertSeconds = seconds
	thresholdsMu.Unlock()
	t.Cleanup(func() {
		thresholdsMu.Lock()
		activeThresholds.StagnationFirstAlertSeconds = orig
		thresholdsMu.Unlock()
	})
}

func countCriticalStuckAlerts(t *testing.T, db *gorm.DB, chainID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&database.AlertLog{}).
		Where("chain_id = ? AND addr = ? AND level = ?", chainID, "all", "CRITICAL").
		Count(&n).Error)
	return n
}

// TestCollectParticipation_AcceptedRegressionThenAdvance_NoFalseStuckAlert
// is the RED/GREEN case for fix 3: a pool failover to a healthy backup a
// few blocks behind the previous primary must never fire "Blockchain
// stuck", even once the regression has persisted long enough to be
// accepted as the new baseline.
//
// Scripted heights: 5 (establishes a baseline) -> 3, 3 (a regression that
// persists across the mandatory 3s "ignore" backoff, long enough to be
// accepted once StagnationFirstAlertSeconds is lowered to 1s) -> 6 (the
// backup keeps advancing normally on the very next poll).
//
// Before the fix, accepting the regression at height 3 fell straight into
// the stalled branch with lastProgressTime untouched since height 5, so
// stuckFor already exceeded the bound on that very line and fired an
// instant CRITICAL — this test fails against that code (RED). The fix
// resets the stagnation clock on acceptance instead, so the very next
// (advancing) poll never reaches the alert check at all (GREEN).
func TestCollectParticipation_AcceptedRegressionThenAdvance_NoFalseStuckAlert(t *testing.T) {
	db := testoutils.NewTestDB(t)
	setStagnationFirstAlertSecondsForTest(t, 1)

	chainID := "test-regress-accept-advance"
	client := newStagnationTestClient([]int64{5, 3, 3, 6})

	ctx, cancel := context.WithCancel(context.Background())
	CollectParticipation(ctx, db, chainID, gnoclient.Client{RPCClient: client})

	require.Eventually(t, func() bool { return client.callCount() >= 5 }, 20*time.Second, 20*time.Millisecond,
		"the scripted sequence (4 heights) must be fully consumed, plus one more call into the error branch")

	assert.Equal(t, int64(6), GetLastHeight(chainID),
		"the accepted regression's baseline must have advanced to the backup's latest real height")
	assert.Equal(t, int64(0), countCriticalStuckAlerts(t, db, chainID),
		"accepting a regression from a healthy, merely-lagging backup must never fire a 'Blockchain stuck' CRITICAL")
	assert.False(t, IsAlertSent(chainID, "all"), "no stuck alert should have been recorded as sent")

	cancel()
	// Let the monitoring goroutine observe ctx.Done() and exit before the
	// next test touches the same package-level state (activeThresholds,
	// lastProgressTime, ...) — avoids a spurious -race report from a
	// goroutine this test started still running into the next test.
	time.Sleep(300 * time.Millisecond)
}

// TestCollectParticipation_AcceptedRegressionThenHalt_StillAlertsCritical
// is the guard-rail for fix 3: it must not reintroduce the suppression bug
// the regression-acceptance guard originally existed to fix. A node
// rewound to an old snapshot and then genuinely halted there must still
// raise "Blockchain stuck" — delayed by one StagnationFirstAlert() window
// (spent resetting the clock on acceptance), never suppressed outright.
//
// Scripted heights: 5 (baseline) -> 3, 3 (regression, accepted after the
// lowered 1s bound) -> 3 (the rewound node stays flat instead of
// advancing): the third poll at height 3 is now a genuine, non-regressed
// stall against the accepted baseline, and must alert once stuckFor
// exceeds the bound.
func TestCollectParticipation_AcceptedRegressionThenHalt_StillAlertsCritical(t *testing.T) {
	db := testoutils.NewTestDB(t)
	setStagnationFirstAlertSecondsForTest(t, 1)

	chainID := "test-regress-accept-halt"
	client := newStagnationTestClient([]int64{5, 3, 3, 3})

	ctx, cancel := context.WithCancel(context.Background())
	CollectParticipation(ctx, db, chainID, gnoclient.Client{RPCClient: client})

	require.Eventually(t, func() bool { return countCriticalStuckAlerts(t, db, chainID) >= 1 }, 20*time.Second, 20*time.Millisecond,
		"a node that rewinds and then genuinely halts at the accepted baseline must still raise 'Blockchain stuck', just one window later than an instant-fire would have")

	assert.True(t, IsAlertSent(chainID, "all"))

	cancel()
	time.Sleep(300 * time.Millisecond)
}
