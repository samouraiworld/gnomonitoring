package gnovalidator

import (
	"fmt"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bftTestThresholds() Thresholds {
	return Thresholds{
		CriticalThreshold: 30, AlertWarningResendHours: 6, AlertCriticalResendHours: 24,
		BFTAlertEnabled: true, BFTAlertCheckMinutes: 5, BFTAlertWindowBlocks: 100,
		BFTAlertWarningLosses: 1, BFTAlertWarningMarginPct: 10, BFTAlertCriticalMarginPct: 3,
	}
}

// bftTestNow is the evaluation time of the pure tests; bftWindow's latest
// block is one block time before it, so the window is never stale.
var bftTestNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// bftWindow builds a fully collected 100-block window: vps gives each
// validator's voting power and signed how many of the 100 blocks it signed.
func bftWindow(vps []int64, signed []int64) database.BFTWindow {
	w := database.BFTWindow{MinHeight: 101, MaxHeight: 200, Heights: 100, LastBlockTime: bftTestNow.Add(-5 * time.Second)}
	for i, vp := range vps {
		w.Entries = append(w.Entries, database.BFTWindowEntry{
			Addr: fmt.Sprintf("g1v%d", i), Moniker: fmt.Sprintf("v%d", i),
			VotingPower: vp, Blocks: 100, Signed: signed[i], TrailingMissed: trailing(signed[i]),
		})
	}
	return w
}

// trailing models a validator's misses as spread over the window, except
// for one that signed nothing: it missed every block, the last ones too.
func trailing(signed int64) int64 {
	if signed == 0 {
		return 100
	}
	return 0
}

func repeat(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestEvaluateBFT_NoSnapshot(t *testing.T) {
	th := bftTestThresholds()

	assert.False(t, evaluateBFT(database.BFTWindow{}, th, bftTestNow).Evaluated, "empty window")
	assert.False(t, evaluateBFT(bftWindow([]int64{0, 0, 0}, []int64{0, 0, 0}), th, bftTestNow).Evaluated,
		"no voting power anywhere means no snapshot yet")
}

func TestEvaluateBFT(t *testing.T) {
	tests := []struct {
		name       string
		vps        []int64
		signed     []int64
		want       bftSeverity
		wantMargin float64
		wantLosses int
		wantStruct int
		wantOff    []string
	}{
		{
			// Devnet 10/10/11: losing crew-3 alone halts the chain, so the
			// complete set already tolerates 0 losses. Structural, no alert.
			name: "small set all online", vps: []int64{10, 10, 11}, signed: []int64{100, 100, 100},
			want: bftOK, wantMargin: 33.3, wantLosses: 0, wantStruct: 0,
		},
		{
			name: "devnet crew-1 down", vps: []int64{10, 10, 11}, signed: []int64{0, 100, 100},
			want: bftCritical, wantMargin: 1.1, wantLosses: 0, wantStruct: 0, wantOff: []string{"v0"},
		},
		{
			// The complete set already could not lose crew-3, but it could
			// lose crew-1 or crew-2. With the 1-VP validator down, losing any
			// of the three halts the chain: more single points of failure.
			name: "tiny validator down on a structurally weak set", vps: []int64{10, 10, 11, 1}, signed: []int64{100, 100, 100, 0},
			want: bftCritical, wantMargin: 30.2, wantLosses: 0, wantStruct: 0, wantOff: []string{"v3"},
		},
		{
			// Losing the 40 always halted the chain; with one 20 down, so
			// does losing any other validator. The margin alone (13.3%)
			// would stay silent.
			name: "concentrated set, a small validator down", vps: []int64{40, 20, 20, 20}, signed: []int64{100, 100, 100, 0},
			want: bftCritical, wantMargin: 13.3, wantLosses: 0, wantStruct: 0, wantOff: []string{"v3"},
		},
		{
			// One validator above 1/3 is a structural risk, not an incident.
			name: "one validator above a third, all online", vps: []int64{34, 33, 33}, signed: []int64{100, 100, 100},
			want: bftOK, wantMargin: 33.3, wantLosses: 0, wantStruct: 0,
		},
		{
			name: "four equal, one down", vps: repeat(10, 4), signed: []int64{100, 100, 100, 0},
			want: bftCritical, wantMargin: 8.3, wantLosses: 0, wantStruct: 1, wantOff: []string{"v3"},
		},
		{
			name: "ten equal, one down", vps: repeat(10, 10), signed: append(repeat(100, 9), 0),
			want: bftOK, wantMargin: 23.3, wantLosses: 2, wantStruct: 3, wantOff: []string{"v9"},
		},
		{
			name: "ten equal, two down", vps: repeat(10, 10), signed: append(repeat(100, 8), 0, 0),
			want: bftWarning, wantMargin: 13.3, wantLosses: 1, wantStruct: 3, wantOff: []string{"v8", "v9"},
		},
		{
			name: "ten equal, three down", vps: repeat(10, 10), signed: append(repeat(100, 7), 0, 0, 0),
			want: bftCritical, wantMargin: 3.3, wantLosses: 0, wantStruct: 3, wantOff: []string{"v7", "v8", "v9"},
		},
		{
			// Many small validators: still 8 losses to spare, but the margin
			// is under 10% of the voting power.
			name: "margin warning with losses to spare", vps: repeat(1, 100), signed: append(repeat(100, 75), repeat(0, 25)...),
			want: bftWarning, wantMargin: 8.3, wantLosses: 8, wantStruct: 33,
		},
		{
			name: "below quorum", vps: repeat(10, 4), signed: []int64{100, 100, 0, 0},
			want: bftCritical, wantMargin: -16.7, wantLosses: 0, wantStruct: 1, wantOff: []string{"v2", "v3"},
		},
		{
			// Exactly 50% signed counts as online (same rule as the report).
			name: "half signed is online", vps: repeat(10, 4), signed: []int64{100, 100, 100, 50},
			want: bftOK, wantMargin: 33.3, wantLosses: 1, wantStruct: 1,
		},
		{
			name: "just under half signed is offline", vps: repeat(10, 4), signed: []int64{100, 100, 100, 49},
			want: bftCritical, wantMargin: 8.3, wantLosses: 0, wantStruct: 1, wantOff: []string{"v3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := evaluateBFT(bftWindow(tt.vps, tt.signed), bftTestThresholds(), bftTestNow)

			require.True(t, ev.Evaluated)
			assert.Equal(t, tt.want, ev.Severity)
			assert.InDelta(t, tt.wantMargin, ev.Live.MarginPercent(), 0.05)
			assert.Equal(t, tt.wantLosses, ev.Live.TolerableLosses, "live tolerable losses")
			assert.Equal(t, tt.wantStruct, ev.Structural.TolerableLosses, "structural tolerable losses")
			if tt.wantOff != nil {
				var got []string
				for _, o := range ev.Offline {
					got = append(got, o.Moniker)
				}
				assert.Equal(t, tt.wantOff, got)
			}
			assert.Equal(t, int64(101), ev.MinHeight)
			assert.Equal(t, int64(200), ev.MaxHeight)
		})
	}
}

func TestEvaluateBFT_OfflineSortedByVotingPower(t *testing.T) {
	ev := evaluateBFT(bftWindow([]int64{1, 10, 5, 50}, []int64{0, 0, 0, 100}), bftTestThresholds(), bftTestNow)

	require.Len(t, ev.Offline, 3)
	assert.Equal(t, []int64{10, 5, 1}, []int64{ev.Offline[0].VotingPower, ev.Offline[1].VotingPower, ev.Offline[2].VotingPower})
}

// Consensus counts a joiner's voting power from its first block, so the
// monitor does too: with no row yet it is offline, and it is online as
// soon as its own rows show it signing.
func TestEvaluateBFT_JoinerStaysInTheTotal(t *testing.T) {
	w := bftWindow(append(repeat(10, 4), 40), append(repeat(100, 4), 0))
	w.Entries[4].Blocks, w.Entries[4].TrailingMissed = 0, 0

	ev := evaluateBFT(w, bftTestThresholds(), bftTestNow)

	assert.Equal(t, int64(80), ev.Live.TotalVP)
	assert.Equal(t, bftCritical, ev.Severity, "40 of 80 VP online is below the quorum")

	w.Entries[4].Blocks, w.Entries[4].Signed = 3, 3
	ev = evaluateBFT(w, bftTestThresholds(), bftTestNow)

	assert.Equal(t, int64(80), ev.Live.OnlineVP)
	assert.Equal(t, bftOK, ev.Severity)
}

// A validator that just stopped is offline once it missed critical_threshold
// blocks in a row at the tip, long before its window-wide rate drops under
// 50%; a shorter hiccup does not count.
func TestEvaluateBFT_StoppedAtTheTipIsOffline(t *testing.T) {
	w := bftWindow(repeat(10, 4), repeat(100, 4))
	w.Entries[3].Signed, w.Entries[3].TrailingMissed = 71, 29

	ev := evaluateBFT(w, bftTestThresholds(), bftTestNow)
	assert.Equal(t, bftOK, ev.Severity, "29 misses in a row is not yet a stop")

	w.Entries[3].TrailingMissed = 30
	ev = evaluateBFT(w, bftTestThresholds(), bftTestNow)
	assert.Equal(t, bftCritical, ev.Severity)
	require.Len(t, ev.Offline, 1)
	assert.Equal(t, "v3", ev.Offline[0].Moniker)

	th := bftTestThresholds()
	th.CriticalThreshold = 0
	assert.Equal(t, bftOK, evaluateBFT(w, th, bftTestNow).Severity, "a zero threshold disables the tip rule")
}

func TestEvaluateBFT_Coverage(t *testing.T) {
	w := bftWindow(repeat(10, 4), []int64{100, 100, 100, 0})
	w.Heights = 79
	assert.False(t, evaluateBFT(w, bftTestThresholds(), bftTestNow).Evaluated,
		"under 80% of the window's heights collected")

	w.Heights = 80
	assert.True(t, evaluateBFT(w, bftTestThresholds(), bftTestNow).Evaluated)

	// A young chain is judged on the heights it can have, not the full window.
	young := bftWindow(repeat(10, 4), repeat(100, 4))
	young.MinHeight, young.MaxHeight, young.Heights = 1, 40, 40
	assert.True(t, evaluateBFT(young, bftTestThresholds(), bftTestNow).Evaluated)
}

// A halted chain freezes the window on its last blocks; drawing a verdict
// from it could resolve an alert in the middle of the outage.
func TestEvaluateBFT_StaleWindowIsNotEvaluated(t *testing.T) {
	th := bftTestThresholds()
	w := bftWindow(repeat(10, 4), repeat(100, 4))

	w.LastBlockTime = bftTestNow.Add(-10 * time.Minute)
	assert.True(t, evaluateBFT(w, th, bftTestNow).Evaluated, "exactly two intervals old is still fresh")

	w.LastBlockTime = bftTestNow.Add(-10*time.Minute - time.Second)
	assert.False(t, evaluateBFT(w, th, bftTestNow).Evaluated)

	// A short interval does not shrink the allowance below 10 minutes.
	th.BFTAlertCheckMinutes = 1
	w.LastBlockTime = bftTestNow.Add(-9 * time.Minute)
	assert.True(t, evaluateBFT(w, th, bftTestNow).Evaluated)
}

func TestDecideBFT(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	th := bftTestThresholds()
	open := func(level string, ago time.Duration) database.BFTAlertState {
		return database.BFTAlertState{Level: level, SentAt: now.Add(-ago)}
	}
	none := database.BFTAlertState{}
	const h = int64(1000)

	tests := []struct {
		name       string
		sev        bftSeverity
		state      database.BFTAlertState
		streak     bftStreak
		wantLevel  string
		wantStreak bftStreak
	}{
		{"healthy, nothing open", bftOK, none, bftStreak{}, "", bftStreak{Height: h}},
		{"healthy after a resolve", bftOK, open(bftLevelResolved, time.Hour), bftStreak{Bad: 1, Height: 900}, "", bftStreak{Height: h}},
		{"new warning", bftWarning, none, bftStreak{}, bftLevelWarning, bftStreak{Height: h}},
		{"new critical long after a resolve", bftCritical, open(bftLevelResolved, 6*time.Hour), bftStreak{}, bftLevelCritical, bftStreak{Height: h}},
		{"first bad check right after a resolve holds", bftCritical, open(bftLevelResolved, time.Hour), bftStreak{Height: 900}, "", bftStreak{Bad: 1, Height: h}},
		{"second bad check right after a resolve sends", bftWarning, open(bftLevelResolved, time.Hour), bftStreak{Bad: 1, Height: 900}, bftLevelWarning, bftStreak{Height: h}},
		{"bad check on a frozen height does not count", bftWarning, open(bftLevelResolved, time.Hour), bftStreak{Bad: 1, Height: h}, "", bftStreak{Bad: 1, Height: h}},
		{"warning inside resend window", bftWarning, open(bftLevelWarning, 5*time.Hour), bftStreak{}, "", bftStreak{Height: h}},
		{"warning after resend window", bftWarning, open(bftLevelWarning, 6*time.Hour), bftStreak{}, bftLevelWarning, bftStreak{Height: h}},
		{"critical inside resend window", bftCritical, open(bftLevelCritical, 23*time.Hour), bftStreak{}, "", bftStreak{Height: h}},
		{"critical after resend window", bftCritical, open(bftLevelCritical, 24*time.Hour), bftStreak{}, bftLevelCritical, bftStreak{Height: h}},
		{"escalation is immediate", bftCritical, open(bftLevelWarning, time.Minute), bftStreak{}, bftLevelCritical, bftStreak{Height: h}},
		{"de-escalation waits for the open level's resend window", bftWarning, open(bftLevelCritical, 23*time.Hour), bftStreak{}, "", bftStreak{Height: h}},
		{"de-escalation after the open level's resend window", bftWarning, open(bftLevelCritical, 24*time.Hour), bftStreak{}, bftLevelWarning, bftStreak{Height: h}},
		{"first healthy check holds", bftOK, open(bftLevelCritical, time.Hour), bftStreak{Height: 900}, "", bftStreak{OK: 1, Height: h}},
		{"second healthy check resolves", bftOK, open(bftLevelWarning, time.Hour), bftStreak{OK: 1, Height: 900}, bftLevelResolved, bftStreak{Height: h}},
		{"healthy check on a frozen height does not count", bftOK, open(bftLevelWarning, time.Hour), bftStreak{OK: 1, Height: h}, "", bftStreak{OK: 1, Height: h}},
		{"relapse resets the streak", bftWarning, open(bftLevelWarning, time.Hour), bftStreak{OK: 1, Height: 900}, "", bftStreak{Height: h}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, streak := decideBFT(tt.sev, h, tt.state, tt.streak, now, th)
			assert.Equal(t, tt.wantLevel, level)
			assert.Equal(t, tt.wantStreak, streak)
		})
	}
}
