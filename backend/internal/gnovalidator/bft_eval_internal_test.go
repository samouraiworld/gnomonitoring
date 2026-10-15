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
		AlertWarningResendHours: 6, AlertCriticalResendHours: 24,
		BFTAlertEnabled: true, BFTAlertCheckMinutes: 5, BFTAlertWindowBlocks: 100,
		BFTAlertWarningLosses: 1, BFTAlertWarningMarginPct: 10, BFTAlertCriticalMarginPct: 3,
	}
}

// bftWindow builds a 100-block window: vps gives each validator's voting
// power and signed how many of the 100 blocks it signed.
func bftWindow(vps []int64, signed []int64) database.BFTWindow {
	w := database.BFTWindow{MinHeight: 101, MaxHeight: 200}
	for i, vp := range vps {
		w.Entries = append(w.Entries, database.BFTWindowEntry{
			Addr: fmt.Sprintf("g1v%d", i), Moniker: fmt.Sprintf("v%d", i),
			VotingPower: vp, Blocks: 100, Signed: signed[i],
		})
	}
	return w
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

	assert.False(t, evaluateBFT(database.BFTWindow{}, th).Evaluated, "empty window")
	assert.False(t, evaluateBFT(bftWindow([]int64{0, 0, 0}, []int64{0, 0, 0}), th).Evaluated,
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
			// A tiny validator down barely moves the margin and the set
			// already tolerated no loss: nothing to report.
			name: "tiny validator down on a structurally weak set", vps: []int64{10, 10, 11, 1}, signed: []int64{100, 100, 100, 0},
			want: bftOK, wantMargin: 30.2, wantLosses: 0, wantStruct: 0, wantOff: []string{"v3"},
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
			ev := evaluateBFT(bftWindow(tt.vps, tt.signed), bftTestThresholds())

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
	ev := evaluateBFT(bftWindow([]int64{1, 10, 5, 50}, []int64{0, 0, 0, 100}), bftTestThresholds())

	require.Len(t, ev.Offline, 3)
	assert.Equal(t, []int64{10, 5, 1}, []int64{ev.Offline[0].VotingPower, ev.Offline[1].VotingPower, ev.Offline[2].VotingPower})
}

func TestEvaluateBFT_MemberWithoutRowsIsOffline(t *testing.T) {
	w := bftWindow(repeat(10, 4), repeat(100, 4))
	w.Entries[3].Blocks, w.Entries[3].Signed = 0, 0

	ev := evaluateBFT(w, bftTestThresholds())

	assert.Equal(t, bftCritical, ev.Severity)
	require.Len(t, ev.Offline, 1)
	assert.Equal(t, "v3", ev.Offline[0].Moniker)
}

func TestDecideBFT(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	th := bftTestThresholds()
	open := func(level string, ago time.Duration) database.BFTAlertState {
		return database.BFTAlertState{Level: level, SentAt: now.Add(-ago)}
	}

	tests := []struct {
		name       string
		sev        bftSeverity
		state      database.BFTAlertState
		streak     int
		wantLevel  string
		wantStreak int
	}{
		{"healthy, nothing open", bftOK, database.BFTAlertState{}, 0, "", 0},
		{"healthy after a resolve", bftOK, open(bftLevelResolved, time.Hour), 0, "", 0},
		{"new warning", bftWarning, database.BFTAlertState{}, 0, bftLevelWarning, 0},
		{"new critical after a resolve", bftCritical, open(bftLevelResolved, time.Minute), 0, bftLevelCritical, 0},
		{"warning inside resend window", bftWarning, open(bftLevelWarning, 5*time.Hour), 0, "", 0},
		{"warning after resend window", bftWarning, open(bftLevelWarning, 6*time.Hour), 0, bftLevelWarning, 0},
		{"critical inside resend window", bftCritical, open(bftLevelCritical, 23*time.Hour), 0, "", 0},
		{"critical after resend window", bftCritical, open(bftLevelCritical, 24*time.Hour), 0, bftLevelCritical, 0},
		{"escalation is immediate", bftCritical, open(bftLevelWarning, time.Minute), 0, bftLevelCritical, 0},
		{"de-escalation is immediate", bftWarning, open(bftLevelCritical, time.Minute), 0, bftLevelWarning, 0},
		{"first healthy check holds", bftOK, open(bftLevelCritical, time.Hour), 0, "", 1},
		{"second healthy check resolves", bftOK, open(bftLevelWarning, time.Hour), 1, bftLevelResolved, 0},
		{"relapse resets the streak", bftWarning, open(bftLevelWarning, time.Hour), 1, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, streak := decideBFT(tt.sev, tt.state, tt.streak, now, th)
			assert.Equal(t, tt.wantLevel, level)
			assert.Equal(t, tt.wantStreak, streak)
		})
	}
}
