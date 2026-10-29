package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func consensusTestChains(t *testing.T) {
	t.Helper()
	oldChains, oldDefault, oldOrigins := internal.Config.Chains, internal.Config.DefaultChain, internal.Config.AllowedOrigins
	internal.Config.Chains = map[string]*internal.ChainConfig{
		"consensus-a": {Enabled: true}, "consensus-b": {Enabled: true}, "consensus-empty": {Enabled: true},
	}
	internal.Config.DefaultChain = "consensus-a"
	internal.Config.AllowedOrigins = []string{"https://memba.club"}
	t.Cleanup(func() {
		internal.Config.Chains, internal.Config.DefaultChain, internal.Config.AllowedOrigins = oldChains, oldDefault, oldOrigins
	})
}

func consensusRequest(t *testing.T, h http.Handler, query string) consensusReport {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/reports/consensus"+query, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var out consensusReport
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func seedConsensus(t *testing.T, db *gorm.DB, chain string, now time.Time, lag int64) {
	t.Helper()
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chain, Addr: "g1active", Moniker: chain, VotingPower: 10}).Error)
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chain, Addr: "g1new", Moniker: "new", VotingPower: 2}).Error)
	rows := []database.DailyParticipation{
		{ChainID: chain, Addr: "g1active", BlockHeight: 1, Date: now.Add(-time.Hour), Participated: true, PrecommitLagMs: &lag},
		{ChainID: chain, Addr: "g1active", BlockHeight: 2, Date: now, Participated: true, PrecommitLagMs: &lag},
		{ChainID: chain, Addr: "g1active", BlockHeight: 3, Date: now.Add(-48 * time.Hour), Participated: true, PrecommitLagMs: &lag},
		{ChainID: chain, Addr: "g1departed", BlockHeight: 2, Date: now, Participated: true, PrecommitLagMs: &lag},
	}
	require.NoError(t, db.Create(&rows).Error)
}

func TestConsensusContractLatencyAndIsolation(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedConsensus(t, db, "consensus-a", now, 20)
	seedConsensus(t, db, "consensus-b", now, 900)
	h := newConsensusReportHandler(db)
	a := consensusRequest(t, h, "?chain=consensus-a")
	require.Equal(t, "consensus-a", a.ChainID)
	require.Equal(t, 30, a.CacheTTLSeconds)
	require.Len(t, a.Validators, 2, "active snapshot members only; include zero-history joiners")
	require.Equal(t, "g1active", a.Validators[0].Addr)
	day := a.Validators[0].PrecommitLag["last_24h"]
	require.NotNil(t, day.P50MS)
	assert.Equal(t, 20.0, *day.P50MS)
	assert.Equal(t, int64(2), day.SampleCount)
	require.NotNil(t, day.LastSampleAt)
	assert.True(t, day.LastSampleAt.Equal(now), "last sample is actual block time")
	assert.Equal(t, int64(3), a.Validators[0].PrecommitLag["last_7d"].SampleCount)
	newDay := a.Validators[1].PrecommitLag["last_24h"]
	assert.Nil(t, newDay.P50MS)
	assert.Nil(t, newDay.P90MS)
	assert.Nil(t, newDay.LastSampleAt)
	assert.Zero(t, newDay.SampleCount)
	b := consensusRequest(t, h, "?chain=consensus-b&addr=g1active")
	require.Len(t, b.Validators, 1)
	assert.Equal(t, 900.0, *b.Validators[0].PrecommitLag["last_24h"].P50MS)
	assert.Empty(t, consensusRequest(t, h, "?addr=active").Validators, "exact address, not substring/moniker search")
	assert.Empty(t, consensusRequest(t, h, "?addr=g1departed").Validators)
	assert.Len(t, consensusRequest(t, h, "?chain=consensus-a").Validators, 2, "filtering never mutates cached roster")
	empty := consensusRequest(t, h, "?chain=consensus-empty")
	assert.Empty(t, empty.Validators)
	assert.Equal(t, "no_data", empty.BFT.Reason)
	assert.False(t, empty.BFT.Available)
	assert.Nil(t, empty.BFT.MarginPercent)
	assert.Nil(t, empty.BFT.TotalVotingPower)
}

func TestConsensusPreservesResolvedMonikerWhenSnapshotNameIsMissing(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	for _, tc := range []struct{ addr, snapshot, historical string }{
		{"g1empty", "", "named-from-history"},
		{"g1unknown", "unknown", "another-historical-name"},
	} {
		require.NoError(t, db.Create(&database.AddrMoniker{
			ChainID: "consensus-a", Addr: tc.addr, Moniker: tc.snapshot, VotingPower: 5,
		}).Error)
		require.NoError(t, db.Create(&database.DailyParticipation{
			ChainID: "consensus-a", Addr: tc.addr, Moniker: tc.historical,
			BlockHeight: 1, Date: now, Participated: true,
		}).Error)
	}
	legacy := httptest.NewRecorder()
	GetValidatorReportHandler(legacy, httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=consensus-a", nil), db)
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	var health []validatorReport
	require.NoError(t, json.Unmarshal(legacy.Body.Bytes(), &health))
	require.Len(t, health, 2)
	healthNames := make(map[string]string, len(health))
	for _, validator := range health {
		healthNames[validator.Addr] = validator.Moniker
	}
	consensus := consensusRequest(t, newConsensusReportHandler(db), "")
	require.Len(t, consensus.Validators, 2)
	for _, validator := range consensus.Validators {
		assert.Equal(t, healthNames[validator.Addr], validator.Moniker)
		assert.NotEmpty(t, validator.Moniker)
		assert.NotEqual(t, "unknown", validator.Moniker)
	}
	assert.Equal(t, "named-from-history", healthNames["g1empty"])
	assert.Equal(t, "another-historical-name", healthNames["g1unknown"])
}

func TestConsensusBFTUsesLiveWindowAndUnavailableReasons(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	// 4 equal validators, two online: margin is negative, never clamped to 0.
	for _, addr := range []string{"g1a", "g1b", "g1c", "g1d"} {
		require.NoError(t, db.Create(&database.AddrMoniker{ChainID: "consensus-a", Addr: addr, VotingPower: 1}).Error)
		for height := int64(1); height <= 100; height++ {
			require.NoError(t, db.Create(&database.DailyParticipation{
				ChainID: "consensus-a", Addr: addr, BlockHeight: height, Date: now,
				Participated: addr == "g1a" || addr == "g1b",
			}).Error)
		}
	}
	out := consensusRequest(t, newConsensusReportHandler(db), "")
	require.True(t, out.BFT.Available)
	require.NotNil(t, out.BFT.MarginPercent)
	assert.InDelta(t, -100.0/6, *out.BFT.MarginPercent, 0.001)
	assert.Equal(t, int64(100), out.BFT.CollectedHeights)
	assert.Equal(t, int64(100), out.BFT.ToHeight)
	assert.Equal(t, int64(2), *out.BFT.OnlineVotingPower)
	assert.Equal(t, int64(4), *out.BFT.TotalVotingPower)

	require.NoError(t, db.Model(&database.DailyParticipation{}).Where("chain_id = ?", "consensus-a").Update("date", now.Add(-time.Hour)).Error)
	stale := consensusRequest(t, newConsensusReportHandler(db), "")
	assert.False(t, stale.BFT.Available)
	assert.Equal(t, "stale", stale.BFT.Reason)
	assert.Nil(t, stale.BFT.MarginPercent)
	assert.NotNil(t, stale.BFT.LastBlockAt)

	require.NoError(t, db.Where("chain_id = ? AND block_height < ?", "consensus-a", 99).Delete(&database.DailyParticipation{}).Error)
	partial := consensusRequest(t, newConsensusReportHandler(db), "")
	assert.False(t, partial.BFT.Available)
	assert.Equal(t, "insufficient_coverage", partial.BFT.Reason)
	assert.Nil(t, partial.BFT.OnlineVotingPower)

	// Fresh blocks without a captured VP snapshot remain unavailable.
	require.NoError(t, db.Create(&database.DailyParticipation{ChainID: "consensus-b", Addr: "g1x", BlockHeight: 1, Date: now, Participated: true}).Error)
	noVP := consensusRequest(t, newConsensusReportHandler(db), "?chain=consensus-b")
	assert.Equal(t, "no_voting_power", noVP.BFT.Reason)
	assert.Nil(t, noVP.BFT.MarginPercent)
}

func TestConsensusCacheExpiryAndDatabaseSeparation(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	seedConsensus(t, db, "consensus-a", now, 0)
	h := newConsensusReportHandler(db)
	first := consensusRequest(t, h, "")
	assert.Equal(t, 0.0, *first.Validators[0].PrecommitLag["last_24h"].P50MS, "measured zero stays numeric")
	require.NoError(t, db.Model(&database.DailyParticipation{}).Where("chain_id = ?", "consensus-a").Update("precommit_lag_ms", 500).Error)
	cached := consensusRequest(t, h, "")
	assert.True(t, cached.ComputedAt.Equal(first.ComputedAt))
	assert.Equal(t, 0.0, *cached.Validators[0].PrecommitLag["last_24h"].P50MS)
	h.mu.Lock()
	snapshot := h.cache["consensus-a"]
	snapshot.ComputedAt = time.Now().Add(-consensusCacheTTL - time.Second)
	h.cache["consensus-a"] = snapshot
	h.mu.Unlock()
	fresh := consensusRequest(t, h, "")
	assert.Equal(t, 500.0, *fresh.Validators[0].PrecommitLag["last_24h"].P50MS)
	otherDB := testoutils.NewTestDB(t)
	assert.Empty(t, consensusRequest(t, newConsensusReportHandler(otherDB), "").Validators)
	// A cold concurrent burst, including different address filters, runs one
	// expensive percentile scan for this chain, rather than one per request.
	var scans atomic.Int64
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:latency_scans", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "AS samples_24h") {
			scans.Add(1)
		}
	}))
	h = newConsensusReportHandler(db)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reports/consensus?addr=g1active", nil))
			assert.Equal(t, http.StatusOK, w.Code)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), scans.Load())
}

func TestConsensusErrorsAreNotCachedAndCancellationIsHonored(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	h := newConsensusReportHandler(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/reports/consensus", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, h.cache)
	assert.NotContains(t, w.Body.String(), "SELECT")
	// A later healthy request retries, rather than caching absence/error.
	assert.Equal(t, "consensus-a", consensusRequest(t, h, "").ChainID)
}

func TestConsensusHTTPMethodsValidationCORSAndRateLimit(t *testing.T) {
	consensusTestChains(t)
	h := newConsensusReportHandler(nil)
	for _, tc := range []struct {
		method, query string
		status        int
	}{
		{http.MethodOptions, "", http.StatusNoContent},
		{http.MethodPost, "", http.StatusMethodNotAllowed},
		{http.MethodGet, "?chain=unknown", http.StatusBadRequest},
		{http.MethodGet, "?addr=%20g1x", http.StatusBadRequest},
		{http.MethodGet, "?addr=" + strings.Repeat("x", 129), http.StatusBadRequest},
	} {
		r := httptest.NewRequest(tc.method, "/api/reports/consensus"+tc.query, nil)
		r.Header.Set("Origin", "https://memba.club")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, tc.status, w.Code)
		assert.Equal(t, "https://memba.club", w.Header().Get("Access-Control-Allow-Origin"))
	}
	limiter, _ := newTestLimiter(1, 1, "")
	limited := limiter.middleware(h)
	assert.Equal(t, http.StatusMethodNotAllowed, doRequest(limited, http.MethodPost, "203.0.113.9:1", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, doRequest(limited, http.MethodPost, "203.0.113.9:1", nil).Code)
}

func TestConsensusCanceledReadersDoNotCancelSharedLoad(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	seedConsensus(t, db, "consensus-a", time.Now().UTC(), 10)
	entered, release := make(chan struct{}), make(chan struct{})
	loadDeadline := make(chan time.Time, 1)
	var once sync.Once
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:block_latency_scan", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "AS samples_24h") {
			once.Do(func() {
				deadline, _ := tx.Statement.Context.Deadline()
				loadDeadline <- deadline
				close(entered)
				<-release
			})
		}
	}))
	h := newConsensusReportHandler(db)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan error, 1)
	go func() {
		_, err := h.snapshot(leaderCtx, "consensus-a")
		leaderDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("shared load did not start")
	}
	deadline := <-loadDeadline
	require.False(t, deadline.IsZero(), "the shared SQL load must have a deadline")
	assert.LessOrEqual(t, time.Until(deadline), consensusQueryTimeout)
	// A short per-reader deadline does not have to wait for the shared SQL.
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	_, err := h.snapshot(waitCtx, "consensus-a")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	cancelLeader()
	select {
	case err := <-leaderDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled leader stayed blocked on shared SQL")
	}
	healthyDone := make(chan error, 1)
	go func() {
		_, err := h.snapshot(context.Background(), "consensus-a")
		healthyDone <- err
	}()
	close(release)
	select {
	case err := <-healthyDone:
		require.NoError(t, err, "the canceled leader must not cancel another reader's SQL")
	case <-time.After(5 * time.Second):
		t.Fatal("healthy waiter did not receive shared result")
	}
	assert.Len(t, h.cache, 1)
}

func TestConsensusLeavesLegacyHealthContractUnchanged(t *testing.T) {
	consensusTestChains(t)
	db := testoutils.NewTestDB(t)
	seedConsensus(t, db, "consensus-a", time.Now().UTC(), 20)
	legacy := func() []byte {
		w := httptest.NewRecorder()
		GetValidatorReportHandler(w, httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=consensus-a", nil), db)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.Bytes()
	}
	before := legacy()
	consensus := consensusRequest(t, newConsensusReportHandler(db), "")
	assert.JSONEq(t, string(before), string(legacy()))
	var health []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &health))
	require.Len(t, health, 2)
	for _, entry := range health {
		assert.Len(t, entry, 4, "no observability fields added to legacy health entries")
		var periods map[string]periodScore
		require.NoError(t, json.Unmarshal(entry["periods"], &periods))
		assert.Len(t, periods, 4)
		for _, key := range reportPeriods {
			_, ok := periods[key]
			assert.True(t, ok, key)
		}
	}
	// Actual handler payloads are useful fixtures for independent consumer tests.
	t.Logf("legacy_health_fixture=%s", before)
	encoded, err := json.Marshal(consensus)
	require.NoError(t, err)
	t.Logf("consensus_fixture=%s", encoded)
}
