package gnovalidator

import "testing"

func TestValsetVotingPower_CopyAndChainIsolation(t *testing.T) {
	src := map[string]int64{"g1a": 10, "g1b": 5}
	setValsetVotingPower("vp-test-a", src)
	src["g1a"] = 999

	got := getValsetVotingPower("vp-test-a")
	if got["g1a"] != 10 || got["g1b"] != 5 {
		t.Fatalf("snapshot = %v, want g1a=10 g1b=5", got)
	}
	got["g1b"] = 999
	if getValsetVotingPower("vp-test-a")["g1b"] != 5 {
		t.Fatal("getValsetVotingPower must return a copy")
	}

	if other := getValsetVotingPower("vp-test-unknown"); len(other) != 0 {
		t.Fatalf("unknown chain = %v, want empty", other)
	}

	setValsetVotingPower("vp-test-a", map[string]int64{"g1c": 1})
	if got := getValsetVotingPower("vp-test-a"); len(got) != 1 || got["g1c"] != 1 {
		t.Fatalf("snapshot must be replaced, not merged: %v", got)
	}
}
