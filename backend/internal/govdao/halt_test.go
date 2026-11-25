package govdao

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeHaltChain is an in-memory chain answering the halt watcher's RPC calls.
// Block i is produced at genesis + i*blockTime.
type fakeHaltChain struct {
	height      int64
	haltHeight  []byte // raw param JSON, nil = never set
	minVersion  []byte
	blockTime   time.Duration
	genesis     time.Time
	queryErr    error
	abciErr     abci.Error
	catchingUp  bool
	prunedBelow int64   // blocks below this height are no longer served
	queried     []int64 // heights the params were queried at
	// pastHalt holds halt_height as it was at a given height, when it
	// differs from the current value.
	pastHalt map[int64][]byte
}

func (f *fakeHaltChain) timeOf(h int64) time.Time {
	return f.genesis.Add(time.Duration(h) * f.blockTime)
}

// now is a moment just after the head block, as a poll would see it.
func (f *fakeHaltChain) now() time.Time {
	return f.timeOf(f.height).Add(5 * time.Second)
}

func (f *fakeHaltChain) setHalt(height int64, version string) {
	f.haltHeight = []byte(fmt.Sprintf("%q", fmt.Sprint(height)))
	f.minVersion = []byte(fmt.Sprintf("%q", version))
}

func (f *fakeHaltChain) Status() (*ctypes.ResultStatus, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{
		LatestBlockHeight: f.height,
		LatestBlockTime:   f.timeOf(f.height),
		CatchingUp:        f.catchingUp,
	}}, nil
}

func (f *fakeHaltChain) ABCIQueryWithOptions(path string, _ []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	f.queried = append(f.queried, opts.Height)
	res := &ctypes.ResultABCIQuery{}
	res.Response.Error = f.abciErr
	switch path {
	case haltHeightParamPath:
		res.Response.Data = f.haltHeight
		if past, ok := f.pastHalt[opts.Height]; ok {
			res.Response.Data = past
		}
	case haltMinVersionParamPath:
		res.Response.Data = f.minVersion
	default:
		return nil, fmt.Errorf("unexpected path %s", path)
	}
	return res, nil
}

func (f *fakeHaltChain) Block(height *int64) (*ctypes.ResultBlock, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if height == nil || *height < max(1, f.prunedBelow) || *height > f.height {
		return nil, fmt.Errorf("no block %v", height)
	}
	return &ctypes.ResultBlock{Block: &types.Block{Header: types.Header{Height: *height, Time: f.timeOf(*height)}}}, nil
}

func TestParseHaltHeight(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int64
		wantErr bool
	}{
		{raw: `"1651500"`, want: 1651500},
		{raw: `1651500`, want: 1651500},
		{raw: `"0"`, want: 0},
		{raw: `"abc"`, wantErr: true},
		{raw: `"-5"`, wantErr: true},
	} {
		got, err := parseHaltHeight([]byte(tc.raw))
		if tc.wantErr {
			assert.Error(t, err, tc.raw)
			continue
		}
		require.NoError(t, err, tc.raw)
		assert.Equal(t, tc.want, got, tc.raw)
	}
	h, err := parseHaltHeight(nil)
	require.NoError(t, err)
	assert.Zero(t, h)
}

func TestParseHaltMinVersion(t *testing.T) {
	v, err := parseHaltMinVersion([]byte(`"v1.9.0"`))
	require.NoError(t, err)
	assert.Equal(t, "v1.9.0", v)

	v, err = parseHaltMinVersion(nil)
	require.NoError(t, err)
	assert.Empty(t, v)

	_, err = parseHaltMinVersion([]byte(`v1.9.0`))
	assert.Error(t, err)
}

func TestObserveHalt(t *testing.T) {
	f := &fakeHaltChain{height: 100, blockTime: 2 * time.Second, genesis: time.Unix(0, 0)}

	obs, err := observeHalt(f)
	require.NoError(t, err)
	assert.Equal(t, int64(0), obs.HaltHeight, "params never set read as no halt")
	assert.False(t, obs.scheduled())

	f.haltHeight, f.minVersion = []byte("null"), []byte("")
	obs, err = observeHalt(f)
	require.NoError(t, err)
	assert.False(t, obs.scheduled())

	f.setHalt(150, "v1.9.0")
	obs, err = observeHalt(f)
	require.NoError(t, err)
	assert.Equal(t, haltObservation{HaltHeight: 150, MinVersion: "v1.9.0", LatestHeight: 100, LatestTime: f.timeOf(100)}, obs)
	assert.True(t, obs.scheduled())

	f.abciErr = abci.StringError("boom")
	_, err = observeHalt(f)
	assert.Error(t, err, "an ABCI error must not read as no halt")

	f.abciErr = nil
	f.queryErr = errors.New("down")
	_, err = observeHalt(f)
	assert.Error(t, err)
}

func TestBlockPace(t *testing.T) {
	f := &fakeHaltChain{height: 10_000, blockTime: 2500 * time.Millisecond, genesis: time.Unix(0, 0)}
	obs, err := observeHalt(f)
	require.NoError(t, err)
	pace, err := blockPace(f, obs)
	require.NoError(t, err)
	assert.Equal(t, 2500*time.Millisecond, pace)

	// A young chain measures over what it has.
	f.height = 11
	obs, _ = observeHalt(f)
	pace, err = blockPace(f, obs)
	require.NoError(t, err)
	assert.Equal(t, 2500*time.Millisecond, pace)

	f.height = 1
	obs, _ = observeHalt(f)
	_, err = blockPace(f, obs)
	assert.Error(t, err)
}

func TestDecideHalt(t *testing.T) {
	th := haltThresholds{Reminder: time.Hour, FinalReminder: 5 * time.Minute}
	pace := 2 * time.Second
	at := func(halt, latest int64, ver string) haltObservation {
		return haltObservation{HaltHeight: halt, MinVersion: ver, LatestHeight: latest, LatestTime: time.Unix(0, 0)}
	}
	announced := func(level string, h int64, ver string) *haltRecord {
		return &haltRecord{ID: 1, Level: level, Height: h, Version: ver}
	}
	sched := func(h int64, ver string) haltState {
		return haltState{Announcement: announced(LevelHaltScheduled, h, ver)}
	}

	for _, tc := range []struct {
		name       string
		obs        haltObservation
		st         haltState
		pace       time.Duration
		wantKind   haltNoticeKind
		wantLevels []string
		wantPrev   int64
	}{
		{name: "nothing set", obs: at(0, 100, "")},
		{name: "past halt still stored (gnoland-1 after v1.8.0)", obs: at(1_651_500, 1_813_047, "v1.8.0")},
		{name: "new halt far away", obs: at(100_000, 100, "v1.9.0"), pace: pace,
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled}},
		{name: "new halt without pace", obs: at(100_000, 100, "v1.9.0"),
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled}},
		{name: "new halt inside reminder window records the reminder", obs: at(1100, 100, "v1.9.0"), pace: pace,
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled, LevelHaltReminder}},
		{name: "new halt inside final window records both reminders", obs: at(200, 100, "v1.9.0"), pace: pace,
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled, LevelHaltReminder, LevelHaltFinalReminder}},
		{name: "already announced, far", obs: at(100_000, 100, "v1.9.0"), st: sched(100_000, "v1.9.0"), pace: pace},
		{name: "already announced, no pace", obs: at(1100, 100, "v1.9.0"), st: sched(1100, "v1.9.0")},
		{name: "reminder", obs: at(1100, 100, "v1.9.0"), st: sched(1100, "v1.9.0"), pace: pace,
			wantKind: haltNoticeReminder, wantLevels: []string{LevelHaltReminder}},
		{name: "reminder already sent", obs: at(1100, 100, "v1.9.0"),
			st: haltState{Announcement: announced(LevelHaltScheduled, 1100, "v1.9.0"), Reminded: true}, pace: pace},
		{name: "final reminder", obs: at(200, 100, "v1.9.0"),
			st: haltState{Announcement: announced(LevelHaltScheduled, 200, "v1.9.0"), Reminded: true}, pace: pace,
			wantKind: haltNoticeFinalReminder, wantLevels: []string{LevelHaltFinalReminder}},
		{name: "final reminder also covers a missed reminder", obs: at(200, 100, "v1.9.0"), st: sched(200, "v1.9.0"), pace: pace,
			wantKind: haltNoticeFinalReminder, wantLevels: []string{LevelHaltFinalReminder, LevelHaltReminder}},
		{name: "final reminder already sent", obs: at(200, 100, "v1.9.0"),
			st: haltState{Announcement: announced(LevelHaltScheduled, 200, "v1.9.0"), Reminded: true, FinalReminded: true}, pace: pace},
		{name: "moved", obs: at(5000, 100, "v1.9.0"), st: sched(4000, "v1.9.0"), pace: pace,
			wantKind: haltNoticeMoved, wantLevels: []string{LevelHaltScheduled}, wantPrev: 4000},
		{name: "version changed", obs: at(4000, 100, "v1.9.1"), st: sched(4000, "v1.9.0"), pace: pace,
			wantKind: haltNoticeVersionChanged, wantLevels: []string{LevelHaltScheduled}, wantPrev: 4000},
		{name: "cancelled", obs: at(0, 100, ""), st: sched(4000, "v1.9.0"),
			wantKind: haltNoticeCancelled, wantLevels: []string{LevelHaltCancelled}},
		{name: "cancel already announced", obs: at(0, 100, ""),
			st: haltState{Announcement: announced(LevelHaltCancelled, 4000, "v1.9.0")}},
		{name: "rescheduled after a cancel", obs: at(4000, 100, "v1.9.0"),
			st: haltState{Announcement: announced(LevelHaltCancelled, 4000, "v1.9.0")}, pace: pace,
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled}},
		{name: "halted, waiting for the upgrade", obs: at(4000, 4000, "v1.9.0"), st: sched(4000, "v1.9.0")},
		{name: "resumed", obs: at(4000, 4001, "v1.9.0"), st: sched(4000, "v1.9.0"),
			wantKind: haltNoticeResumed, wantLevels: []string{LevelHaltResumed}},
		{name: "resume already announced", obs: at(4000, 4100, "v1.9.0"),
			st: haltState{Announcement: announced(LevelHaltScheduled, 4000, "v1.9.0"), Resumed: true}},
		{name: "missed resume reported before a newer halt", obs: at(9000, 4100, "v1.10.0"), st: sched(4000, "v1.9.0"), pace: pace,
			wantKind: haltNoticeResumed, wantLevels: []string{LevelHaltResumed}},
		{name: "moved to a height already reached between two polls", obs: at(3000, 3000, "v1.9.0"), st: sched(5000, "v1.9.0"),
			wantKind: haltNoticeMoved, wantLevels: []string{LevelHaltScheduled}, wantPrev: 5000},
		{name: "node behind the last notice", obs: at(0, 100, ""),
			st: haltState{Announcement: announced(LevelHaltScheduled, 4000, "v1.9.0"), LastObserved: 150}},
		{name: "next halt after a resume", obs: at(9000, 4100, "v1.10.0"),
			st: haltState{Announcement: announced(LevelHaltScheduled, 4000, "v1.9.0"), Resumed: true}, pace: pace,
			wantKind: haltNoticeScheduled, wantLevels: []string{LevelHaltScheduled}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := decideHalt(tc.obs, tc.st, tc.pace, th)
			if tc.wantKind == 0 {
				assert.Nil(t, n)
				return
			}
			require.NotNil(t, n)
			assert.Equal(t, tc.wantKind, n.Kind)
			assert.Equal(t, tc.wantLevels, n.Levels)
			assert.Equal(t, tc.wantPrev, n.PrevHeight)
		})
	}
}

func TestDecideHaltFarHaltHasNoETA(t *testing.T) {
	// A far-future height must not overflow the ETA into a negative duration
	// that would fold both reminders into the announcement.
	obs := haltObservation{HaltHeight: 1 << 62, LatestHeight: 100}
	n := decideHalt(obs, haltState{}, 5*time.Second, haltThresholds{Reminder: time.Hour, FinalReminder: 5 * time.Minute})
	require.NotNil(t, n)
	assert.False(t, n.HasETA)
	assert.Equal(t, []string{LevelHaltScheduled}, n.Levels)
}

func TestParseHaltMinVersionRejectsMarkup(t *testing.T) {
	for _, raw := range []string{`"@everyone"`, `"<!channel>"`, `"v1\nv2"`, `"[x](https://evil)"`, fmt.Sprintf("%q", strings.Repeat("v", 65))} {
		v, err := parseHaltMinVersion([]byte(raw))
		require.NoError(t, err, raw)
		assert.Contains(t, v, "(sanitized)", raw)
		assert.NotContains(t, v, "@", raw)
		assert.NotContains(t, v, "<", raw)
		assert.NotContains(t, v, "\n", raw)
		assert.NotContains(t, v, "](", raw)
		assert.LessOrEqual(t, len(v), maxHaltVersionLen+len("... (sanitized)"), raw)
	}
	v, err := parseHaltMinVersion([]byte(`"@everyone"`))
	require.NoError(t, err)
	assert.Equal(t, "?everyone (sanitized)", v)

	v, err = parseHaltMinVersion([]byte(`"chain/gnoland1.1"`))
	require.NoError(t, err)
	assert.Equal(t, "chain/gnoland1.1", v)
}

func TestObserveHaltReadsParamsAtHead(t *testing.T) {
	f := &fakeHaltChain{height: 100, blockTime: time.Second, genesis: time.Unix(0, 0)}
	f.setHalt(150, "v1.9.0")
	_, err := observeHalt(f)
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 100}, f.queried)

	f.catchingUp = true
	_, err = observeHalt(f)
	assert.Error(t, err, "a syncing node sees an old state")
}

func TestBlockPaceFallsBackOnPrunedHistory(t *testing.T) {
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(0, 0), prunedBelow: 9_500}
	obs, err := observeHalt(f)
	require.NoError(t, err)
	pace, err := blockPace(f, obs)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, pace)
}

func TestDecideHaltDisabledReminders(t *testing.T) {
	obs := haltObservation{HaltHeight: 200, MinVersion: "v1.9.0", LatestHeight: 100}
	st := haltState{Announcement: &haltRecord{ID: 1, Level: LevelHaltScheduled, Height: 200, Version: "v1.9.0"}}
	assert.Nil(t, decideHalt(obs, st, 2*time.Second, haltThresholds{}))
}

func TestFormatHaltDuration(t *testing.T) {
	assert.Equal(t, "40s", formatHaltDuration(40*time.Second))
	assert.Equal(t, "12m", formatHaltDuration(12*time.Minute+10*time.Second))
	assert.Equal(t, "5h 12m", formatHaltDuration(5*time.Hour+12*time.Minute))
	assert.Equal(t, "2d 3h 0m", formatHaltDuration(51*time.Hour))
}

func TestFormatHaltMessages(t *testing.T) {
	n := &haltNotice{
		Kind: haltNoticeScheduled, Height: 9000, Version: "v1.9.0",
		Obs:       haltObservation{LatestHeight: 1800, LatestTime: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)},
		Remaining: 5 * time.Hour, HasETA: true, Pace: 2500 * time.Millisecond,
	}
	plain := formatHaltPlain(n)
	assert.Contains(t, plain, "Chain halt scheduled by GovDAO")
	assert.Contains(t, plain, "Halt height: 9000 (current 1800, 7200 blocks left)")
	assert.Contains(t, plain, "~5h 0m (around 2026-11-25 15:00 UTC, at 2.50s/block)")
	assert.Contains(t, plain, "Minimum version to resume: v1.9.0")

	n.Version = "<v2&>"
	tg := formatHaltTelegram("a&b", n)
	assert.Contains(t, tg, "[a&amp;b]")
	assert.Contains(t, tg, "&lt;v2&amp;&gt;")
	assert.NotContains(t, tg, "<v2")

	n.Kind, n.HasETA = haltNoticeResumed, false
	n.Version = "v1.9.0"
	assert.Contains(t, formatHaltPlain(n), "Chain resumed after the halt at height 9000")

	n.Kind, n.Version = haltNoticeScheduled, ""
	assert.Contains(t, formatHaltPlain(n), "Minimum version to resume: none set")
	n.Obs.LatestHeight = 9000
	assert.Contains(t, formatHaltPlain(n), "Halt height: 9000 (already reached, current 9000)")
}

// sentHalt records the notices checkChainHalt dispatched.
func stubHaltNotify(t *testing.T) *[]*haltNotice {
	t.Helper()
	var sent []*haltNotice
	prev := notifyHalt
	notifyHalt = func(_ *gorm.DB, _ string, n *haltNotice) { sent = append(sent, n) }
	t.Cleanup(func() { notifyHalt = prev })
	return &sent
}

func kinds(ns []*haltNotice) []haltNoticeKind {
	out := make([]haltNoticeKind, len(ns))
	for i, n := range ns {
		out[i] = n.Kind
	}
	return out
}

// TestCheckChainHaltLifecycle drives a halt from its scheduling to the chain
// resuming, re-reading the state from the DB on every check as a restarted
// process would, and checks each notice goes out once.
func TestCheckChainHaltLifecycle(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	check := func() {
		_ = checkChainHalt(db, "test", f, f.now())
	}

	check()
	f.setHalt(9_000, "v1.8.0") // a past halt still stored
	check()
	assert.Empty(t, *sent)

	// Scheduled 5h ahead: 9000 blocks at 2s.
	f.setHalt(19_000, "v1.9.0")
	check()
	check()
	require.Equal(t, []haltNoticeKind{haltNoticeScheduled}, kinds(*sent))
	assert.Equal(t, 5*time.Hour, (*sent)[0].Remaining)

	// Moved later.
	f.setHalt(20_000, "v1.9.0")
	check()
	require.Equal(t, haltNoticeMoved, (*sent)[1].Kind)
	assert.Equal(t, int64(19_000), (*sent)[1].PrevHeight)

	// 50 minutes left, then 4 minutes left.
	f.height = 20_000 - 1500
	check()
	check()
	f.height = 20_000 - 120
	check()
	check()
	// Halted at the halt height.
	f.height = 20_000
	check()
	// No block for over a minute: the halt is reached.
	_ = checkChainHalt(db, "test", f, f.timeOf(f.height).Add(2*time.Minute))
	_ = checkChainHalt(db, "test", f, f.timeOf(f.height).Add(20*time.Minute))
	// Resumed on the new binary; the param keeps the past height.
	f.height = 20_005
	check()
	check()
	assert.Equal(t, []haltNoticeKind{
		haltNoticeScheduled, haltNoticeMoved, haltNoticeReminder, haltNoticeFinalReminder, haltNoticeReached, haltNoticeResumed,
	}, kinds(*sent))

	var rows []database.AlertLog
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", "test", haltAlertAddr).Order("id").Find(&rows).Error)
	var levels []string
	for _, r := range rows {
		levels = append(levels, fmt.Sprintf("%s@%d/%s", r.Level, r.StartHeight, r.Moniker))
		assert.True(t, r.Skipped)
		assert.Positive(t, r.EndHeight, "end_height holds the head the notice was decided at")
	}
	assert.Equal(t, []string{
		"HALT_SCHEDULED@19000/v1.9.0", "HALT_SCHEDULED@20000/v1.9.0", "HALT_REMINDER@20000/v1.9.0",
		"HALT_FINAL_REMINDER@20000/v1.9.0", "HALT_REACHED@20000/v1.9.0", "HALT_RESUMED@20000/v1.9.0",
	}, levels)
}

func TestCheckChainHaltCancel(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	f.setHalt(10_050, "v1.9.0") // 100s ahead: inside both reminder windows
	_ = checkChainHalt(db, "test", f, f.now())
	f.height = 10_040
	_ = checkChainHalt(db, "test", f, f.now())
	f.setHalt(0, "")
	_ = checkChainHalt(db, "test", f, f.now())
	_ = checkChainHalt(db, "test", f, f.now())

	assert.Equal(t, []haltNoticeKind{haltNoticeScheduled, haltNoticeCancelled}, kinds(*sent))
	assert.True(t, strings.Contains(formatHaltPlain((*sent)[1]), "height 10050 cancelled"))

	// The folded reminders are recorded, not dispatched.
	var folded int64
	require.NoError(t, db.Model(&database.AlertLog{}).
		Where("chain_id = ? AND addr = ? AND skipped = false", "test", haltAlertAddr).Count(&folded).Error)
	assert.Equal(t, int64(2), folded)

	// Other chains are independent.
	st, err := loadHaltState(db, "other")
	require.NoError(t, err)
	assert.Nil(t, st.Announcement)
}

func TestCheckChainHaltReadErrorSendsNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	f.setHalt(19_000, "v1.9.0")
	_ = checkChainHalt(db, "test", f, f.now())
	require.Len(t, *sent, 1)

	// An unreadable param must not read as a cancellation.
	f.abciErr = abci.StringError("boom")
	_ = checkChainHalt(db, "test", f, f.now())
	assert.Len(t, *sent, 1)
}

func TestCheckChainHaltIgnoresNodeBehind(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	f.setHalt(19_000, "v1.9.0")
	_ = checkChainHalt(db, "test", f, f.now())
	require.Len(t, *sent, 1)

	// The pool fails over to a node whose state predates the proposal.
	f.height = 9_900
	f.setHalt(0, "")
	_ = checkChainHalt(db, "test", f, f.now())
	assert.Len(t, *sent, 1, "a node behind must not read as a cancellation")
}

// A halt moved later while gnomonitoring was down: the chain went past the
// announced height without stopping, so it is not reported as resumed.
func TestCheckChainHaltMissedMoveIsNotAResume(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	f.setHalt(20_000, "v1.9.0")
	_ = checkChainHalt(db, "test", f, f.now())

	f.setHalt(30_000, "v1.9.0")
	f.pastHalt = map[int64][]byte{20_000: []byte(`"30000"`)}
	f.height = 20_500
	_ = checkChainHalt(db, "test", f, f.now())
	_ = checkChainHalt(db, "test", f, f.now())

	assert.Equal(t, []haltNoticeKind{haltNoticeScheduled, haltNoticeMoved}, kinds(*sent))
	assert.Equal(t, int64(30_000), (*sent)[1].Height)
	assert.Equal(t, int64(20_000), (*sent)[1].PrevHeight)
	assert.True(t, (*sent)[1].HasETA)

	// A cancel missed the same way is reported as one.
	f.setHalt(0, "")
	f.pastHalt = map[int64][]byte{30_000: []byte(`"0"`)}
	f.height = 30_100
	_ = checkChainHalt(db, "test", f, f.now())
	assert.Equal(t, haltNoticeCancelled, (*sent)[2].Kind)
	assert.Equal(t, int64(30_000), (*sent)[2].Height)
}

// A halt first seen when the chain already sits at the halt height (set a few
// blocks ahead, or the watcher was down) is still announced, then resumed.
func TestCheckChainHaltReachedOnFirstSight(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(1_700_000_000, 0)}
	f.setHalt(10_000, "v1.9.0")
	_ = checkChainHalt(db, "test", f, f.now())
	_ = checkChainHalt(db, "test", f, f.now())
	require.Equal(t, []haltNoticeKind{haltNoticeScheduled}, kinds(*sent))
	assert.Contains(t, formatHaltPlain((*sent)[0]), "already reached")

	f.height = 10_003
	_ = checkChainHalt(db, "test", f, f.now())
	assert.Equal(t, []haltNoticeKind{haltNoticeScheduled, haltNoticeResumed}, kinds(*sent))
}

func TestCheckChainHaltReturnsReadError(t *testing.T) {
	db := testoutils.NewTestDB(t)
	stubHaltNotify(t)
	f := &fakeHaltChain{height: 10_000, blockTime: 2 * time.Second, genesis: time.Unix(0, 0), abciErr: abci.StringError("module not registered")}
	assert.Error(t, checkChainHalt(db, "test", f, f.now()))
}

func TestDecideHaltStaleHead(t *testing.T) {
	th := haltThresholds{Reminder: time.Hour, FinalReminder: 5 * time.Minute}
	// A stuck node still showing an old halt ahead: nothing is announced.
	obs := haltObservation{HaltHeight: 1_651_500, MinVersion: "v1.8.0", LatestHeight: 1_651_400, Age: time.Hour}
	assert.Nil(t, decideHalt(obs, haltState{}, 2*time.Second, th))

	// A stuck node at an old halt height it never passed: not a new halt.
	obs = haltObservation{HaltHeight: 1_651_500, MinVersion: "v1.8.0", LatestHeight: 1_651_500, Age: time.Hour}
	assert.Nil(t, decideHalt(obs, haltState{}, 2*time.Second, th))

	// The announced halt, held for an hour: still reported as reached.
	st := haltState{Announcement: &haltRecord{ID: 1, Level: LevelHaltScheduled, Height: 1_651_500, Version: "v1.8.0"}, Reminded: true}
	n := decideHalt(obs, st, 2*time.Second, th)
	require.NotNil(t, n)
	assert.Equal(t, haltNoticeReached, n.Kind)
	assert.Equal(t, []string{LevelHaltReached, LevelHaltFinalReminder}, n.Levels)
	assert.Contains(t, formatHaltPlain(n), "Chain halted at height 1651500 as scheduled")
}
