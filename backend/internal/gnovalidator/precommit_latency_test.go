package gnovalidator

import (
	"testing"
	"time"
)

func pcAt(addr string, base time.Time, nanos int) signedPrecommit {
	return signedPrecommit{Addr: addr, Timestamp: base.Add(time.Duration(nanos))}
}

func requireLate(t *testing.T, got map[string]precommitLatency, addr string, want bool) {
	t.Helper()
	l, ok := got[addr]
	if !ok {
		t.Fatalf("%s missing from result", addr)
	}
	if l.LateForQuorum == nil {
		t.Fatalf("%s LateForQuorum = nil, want %v", addr, want)
	}
	if *l.LateForQuorum != want {
		t.Errorf("%s LateForQuorum = %v, want %v", addr, *l.LateForQuorum, want)
	}
}

// Fixture from gnoland-1 block 60125 (precommits for 60124).
func TestComputePrecommitLatency_Gnoland1Block60125(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	pcs := []signedPrecommit{
		pcAt("samourai", base, 476535857),
		pcAt("berty", base, 417297027),
		pcAt("gno-core", base, 455527698),
		pcAt("onbloc", base, 455970223),
	}
	vp := map[string]int64{"berty": 1, "gno-core": 1, "onbloc": 1, "samourai": 1}

	got := computePrecommitLatency(pcs, vp)

	wantLag := map[string]int64{"berty": 0, "gno-core": 38, "onbloc": 38, "samourai": 59}
	for addr, lag := range wantLag {
		if got[addr].LagMs != lag {
			t.Errorf("%s LagMs = %d, want %d", addr, got[addr].LagMs, lag)
		}
	}
	requireLate(t, got, "berty", false)
	requireLate(t, got, "gno-core", false)
	requireLate(t, got, "onbloc", false)
	requireLate(t, got, "samourai", true)
}

func TestComputePrecommitLatency_UnequalPower(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	pcs := []signedPrecommit{
		pcAt("small1", base, int(10*time.Millisecond)),
		pcAt("big", base, 0),
		pcAt("small2", base, int(20*time.Millisecond)),
	}
	vp := map[string]int64{"big": 10, "small1": 1, "small2": 1}

	got := computePrecommitLatency(pcs, vp)

	requireLate(t, got, "big", false)
	requireLate(t, got, "small1", true)
	requireLate(t, got, "small2", true)
	if got["small2"].LagMs != 20 {
		t.Errorf("small2 LagMs = %d, want 20", got["small2"].LagMs)
	}
}

func TestComputePrecommitLatency_AbsentValidatorCountsInTotal(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	pcs := []signedPrecommit{pcAt("a", base, 0), pcAt("b", base, 1e6), pcAt("c", base, 2e6)}
	vp := map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1}

	got := computePrecommitLatency(pcs, vp)

	if _, ok := got["d"]; ok {
		t.Error("non-signer must not appear in the result")
	}
	requireLate(t, got, "c", false)
}

func TestComputePrecommitLatency_IdenticalTimestamps(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	pcs := []signedPrecommit{pcAt("a", base, 0), pcAt("b", base, 0), pcAt("c", base, 0), pcAt("d", base, 0)}
	vp := map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1}

	got := computePrecommitLatency(pcs, vp)

	for _, addr := range []string{"a", "b", "c", "d"} {
		if got[addr].LagMs != 0 {
			t.Errorf("%s LagMs = %d, want 0", addr, got[addr].LagMs)
		}
		requireLate(t, got, addr, false)
	}
}

func TestComputePrecommitLatency_SingleValidator(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	got := computePrecommitLatency([]signedPrecommit{pcAt("solo", base, 0)}, map[string]int64{"solo": 5})

	if got["solo"].LagMs != 0 {
		t.Errorf("LagMs = %d, want 0", got["solo"].LagMs)
	}
	requireLate(t, got, "solo", false)
}

func TestComputePrecommitLatency_UnknownVotingPowerLeavesLateNil(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	pcs := []signedPrecommit{pcAt("a", base, 0), pcAt("b", base, int(5*time.Millisecond))}

	cases := map[string]map[string]int64{
		"no snapshot":            nil,
		"signer missing from vp": {"a": 1},
		"signer with zero vp":    {"a": 1, "b": 0},
	}
	for name, vp := range cases {
		t.Run(name, func(t *testing.T) {
			got := computePrecommitLatency(pcs, vp)
			if got["b"].LagMs != 5 {
				t.Errorf("LagMs = %d, want 5 (lag is computed without VP)", got["b"].LagMs)
			}
			for addr, l := range got {
				if l.LateForQuorum != nil {
					t.Errorf("%s LateForQuorum = %v, want nil", addr, *l.LateForQuorum)
				}
			}
		})
	}
}

func TestComputePrecommitLatency_Empty(t *testing.T) {
	if got := computePrecommitLatency(nil, map[string]int64{"a": 1}); len(got) != 0 {
		t.Errorf("want empty result, got %+v", got)
	}
}
