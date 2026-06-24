package gnovalidator

import (
	"strings"
	"testing"
)

func TestComputeBFTMargin(t *testing.T) {
	eqSet := func(n int) []ValidatorInfo {
		s := make([]ValidatorInfo, n)
		for i := range s {
			s[i] = ValidatorInfo{Address: string(rune('a' + i)), VotingPower: 1}
		}
		return s
	}
	allActive := func(set []ValidatorInfo) map[string]ValidatorRate {
		m := make(map[string]ValidatorRate, len(set))
		for _, v := range set {
			m[v.Address] = ValidatorRate{Rate: 100}
		}
		return m
	}

	cases := []struct {
		name          string
		set           []ValidatorInfo
		rates         map[string]ValidatorRate
		wantActive    int
		wantTotal     int
		wantTolerable int
		wantTotalPow  int64
		wantActivePow int64
		wantRequired  int64
	}{
		{
			// 6 equal validators, all active. required = 6*2/3+1 = 5.
			// Losing 1 -> 5 online (>4) ok; losing 2 -> 4 (not >4) halts.
			name: "equal power all active tolerates one", set: eqSet(6), rates: allActive(eqSet(6)),
			wantActive: 6, wantTotal: 6, wantTolerable: 1, wantTotalPow: 6, wantActivePow: 6, wantRequired: 5,
		},
		{
			// 6-validator set, only 5 active. required = 5. active = 5.
			// Removing any one drops to 4 < 5 -> tolerate 0 (the issue's
			// "tolerate 1" example is imprecise; Tendermint needs >2/3).
			name: "five of six active tolerates zero",
			set:  eqSet(6),
			rates: func() map[string]ValidatorRate {
				m := allActive(eqSet(6))
				delete(m, "f")
				return m
			}(),
			wantActive: 5, wantTotal: 6, wantTolerable: 0, wantTotalPow: 6, wantActivePow: 5, wantRequired: 5,
		},
		{
			// Below quorum already: 4 of 6 active, active 4 < required 5.
			name: "below quorum",
			set:  eqSet(6),
			rates: map[string]ValidatorRate{
				"a": {Rate: 100}, "b": {Rate: 100}, "c": {Rate: 100}, "d": {Rate: 100},
			},
			wantActive: 4, wantTotal: 6, wantTolerable: 0, wantTotalPow: 6, wantActivePow: 4, wantRequired: 5,
		},
		{
			// Unequal: one whale holds the majority. total=13, required=9.
			// Removing the power-10 validator drops to 3 < 9 -> tolerate 0.
			name: "unequal whale tolerates zero",
			set:  []ValidatorInfo{{Address: "a", VotingPower: 10}, {Address: "b", VotingPower: 1}, {Address: "c", VotingPower: 1}, {Address: "d", VotingPower: 1}},
			rates: map[string]ValidatorRate{
				"a": {Rate: 100}, "b": {Rate: 100}, "c": {Rate: 100}, "d": {Rate: 100},
			},
			wantActive: 4, wantTotal: 4, wantTolerable: 0, wantTotalPow: 13, wantActivePow: 13, wantRequired: 9,
		},
		{
			// Unequal balanced: 5x power 3. total=15, required=11.
			// Remove one (3) -> 12 >= 11 ok; remove two -> 9 < 11 -> tolerate 1.
			name: "unequal balanced tolerates one",
			set:  []ValidatorInfo{{Address: "a", VotingPower: 3}, {Address: "b", VotingPower: 3}, {Address: "c", VotingPower: 3}, {Address: "d", VotingPower: 3}, {Address: "e", VotingPower: 3}},
			rates: map[string]ValidatorRate{
				"a": {Rate: 100}, "b": {Rate: 100}, "c": {Rate: 100}, "d": {Rate: 100}, "e": {Rate: 100},
			},
			wantActive: 5, wantTotal: 5, wantTolerable: 1, wantTotalPow: 15, wantActivePow: 15, wantRequired: 11,
		},
		{
			// A rate entry whose address is not in the validator set has no
			// known power and must be excluded from the active power tally.
			name: "rate without validatorset meta is excluded",
			set:  []ValidatorInfo{{Address: "a", VotingPower: 1}, {Address: "b", VotingPower: 1}},
			rates: map[string]ValidatorRate{
				"a": {Rate: 100}, "b": {Rate: 100}, "c": {Rate: 100},
			},
			wantActive: 2, wantTotal: 2, wantTolerable: 0, wantTotalPow: 2, wantActivePow: 2, wantRequired: 2,
		},
		{
			// rate == 0 means the validator did not participate -> not active.
			name: "zero rate is not active",
			set:  eqSet(3),
			rates: map[string]ValidatorRate{
				"a": {Rate: 100}, "b": {Rate: 100}, "c": {Rate: 0},
			},
			wantActive: 2, wantTotal: 3, wantTolerable: 0, wantTotalPow: 3, wantActivePow: 2, wantRequired: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := ComputeBFTMargin(tc.set, tc.rates)
			if m.ActiveCount != tc.wantActive {
				t.Errorf("ActiveCount = %d, want %d", m.ActiveCount, tc.wantActive)
			}
			if m.TotalCount != tc.wantTotal {
				t.Errorf("TotalCount = %d, want %d", m.TotalCount, tc.wantTotal)
			}
			if m.TolerableOffline != tc.wantTolerable {
				t.Errorf("TolerableOffline = %d, want %d", m.TolerableOffline, tc.wantTolerable)
			}
			if m.TotalPower != tc.wantTotalPow {
				t.Errorf("TotalPower = %d, want %d", m.TotalPower, tc.wantTotalPow)
			}
			if m.ActivePower != tc.wantActivePow {
				t.Errorf("ActivePower = %d, want %d", m.ActivePower, tc.wantActivePow)
			}
			if m.RequiredPower != tc.wantRequired {
				t.Errorf("RequiredPower = %d, want %d", m.RequiredPower, tc.wantRequired)
			}
		})
	}
}

func TestFormatBFTMarginLine(t *testing.T) {
	// No validator-set power known -> omit the line entirely.
	if got := FormatBFTMarginLine(BFTMargin{TotalPower: 0}); got != "" {
		t.Errorf("expected empty line when TotalPower==0, got %q", got)
	}

	healthy := FormatBFTMarginLine(BFTMargin{ActiveCount: 5, TotalCount: 5, TolerableOffline: 2, TotalPower: 15, ActivePower: 15, RequiredPower: 11})
	if !strings.Contains(healthy, "🟢") || !strings.Contains(healthy, "tolerate 2 more") {
		t.Errorf("healthy line wrong: %q", healthy)
	}

	warn := FormatBFTMarginLine(BFTMargin{ActiveCount: 6, TotalCount: 6, TolerableOffline: 1, TotalPower: 6, ActivePower: 6, RequiredPower: 5})
	if !strings.Contains(warn, "⚠️") || !strings.Contains(warn, "tolerate 1 more") {
		t.Errorf("warn line wrong: %q", warn)
	}

	danger := FormatBFTMarginLine(BFTMargin{ActiveCount: 5, TotalCount: 6, TolerableOffline: 0, TotalPower: 6, ActivePower: 5, RequiredPower: 5})
	if !strings.Contains(danger, "🔴") {
		t.Errorf("danger line should carry 🔴: %q", danger)
	}
	if !strings.Contains(danger, "5/6") {
		t.Errorf("danger line should show active/total: %q", danger)
	}
}

func TestBFTAlertLevel(t *testing.T) {
	cases := []struct {
		name string
		m    BFTMargin
		want string
	}{
		{"healthy tolerates two", BFTMargin{TotalCount: 6, TolerableOffline: 2, TotalPower: 6}, ""},
		{"warning tolerates one", BFTMargin{TotalCount: 6, TolerableOffline: 1, TotalPower: 6}, "WARNING"},
		{"critical tolerates zero", BFTMargin{TotalCount: 6, TolerableOffline: 0, TotalPower: 6}, "CRITICAL"},
		{"small set never alerts even at zero", BFTMargin{TotalCount: 3, TolerableOffline: 0, TotalPower: 3}, ""},
		{"small set at warning margin never alerts", BFTMargin{TotalCount: 3, TolerableOffline: 1, TotalPower: 3}, ""},
		{"no power never alerts", BFTMargin{TotalCount: 6, TolerableOffline: 0, TotalPower: 0}, ""},
		{"exactly four validators can alert", BFTMargin{TotalCount: 4, TolerableOffline: 0, TotalPower: 4}, "CRITICAL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BFTAlertLevel(tc.m); got != tc.want {
				t.Errorf("BFTAlertLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBFTAlertTransition(t *testing.T) {
	cases := []struct {
		prev, current, want string
	}{
		{"", "", ""},                  // healthy, stays healthy
		{"", "WARNING", "alert"},      // entering warning
		{"", "CRITICAL", "alert"},     // entering critical
		{"WARNING", "CRITICAL", "alert"}, // escalation
		{"CRITICAL", "WARNING", "alert"}, // de-escalation still re-notifies
		{"WARNING", "WARNING", ""},    // unchanged, no spam
		{"CRITICAL", "CRITICAL", ""},  // unchanged, no spam
		{"WARNING", "", "resolve"},    // recovered from warning
		{"CRITICAL", "", "resolve"},   // recovered from critical
	}
	for _, tc := range cases {
		t.Run(tc.prev+"->"+tc.current, func(t *testing.T) {
			if got := bftAlertTransition(tc.prev, tc.current); got != tc.want {
				t.Errorf("bftAlertTransition(%q,%q) = %q, want %q", tc.prev, tc.current, got, tc.want)
			}
		})
	}
}
