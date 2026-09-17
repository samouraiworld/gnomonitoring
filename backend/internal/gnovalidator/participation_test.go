package gnovalidator

import (
	"testing"
	"time"
)

func TestBuildParticipation_ProposerCreditedWithoutPrecommit(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// proposer "g1prop" did not precommit the previous block (e.g. it just
	// came back online and was immediately selected to propose).
	got := buildParticipation([]string{"g1a", "g1b"}, "g1prop", false, ts, nil)

	p, ok := got["g1prop"]
	if !ok {
		t.Fatal("proposer entry missing")
	}
	if !p.Proposed {
		t.Error("proposer must be credited with Proposed=true even absent from precommits")
	}
	if p.Participated {
		t.Error("proposer absent from precommits must not be marked Participated")
	}
}

func TestBuildParticipation_ProposerAlsoPrecommitted(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	got := buildParticipation([]string{"g1prop", "g1b"}, "g1prop", true, ts, nil)

	p := got["g1prop"]
	if !p.Proposed || !p.Participated || !p.TxContribution {
		t.Errorf("proposer entry = %+v, want Proposed=Participated=TxContribution=true", p)
	}
	if got["g1b"].Proposed {
		t.Error("non-proposer signer must not be marked Proposed")
	}
	if got["g1b"].TxContribution {
		t.Error("non-proposer signer must not be marked TxContribution")
	}
}

func TestBuildParticipation_NonProposerSigner(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	got := buildParticipation([]string{"g1a"}, "g1prop", true, ts, nil)

	if p := got["g1a"]; !p.Participated || p.Proposed || p.TxContribution {
		t.Errorf("signer entry = %+v, want Participated=true, Proposed=false, TxContribution=false", p)
	}
}

func TestBuildParticipation_EmptyPrecommits(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	got := buildParticipation(nil, "g1prop", false, ts, nil)

	if len(got) != 1 {
		t.Fatalf("want exactly the proposer entry, got %+v", got)
	}
	if p := got["g1prop"]; !p.Proposed || p.Participated {
		t.Errorf("proposer entry = %+v, want Proposed=true, Participated=false", p)
	}
}

func TestBuildParticipation_AttachesLatencyToSigners(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	late := true
	latency := map[string]precommitLatency{"g1a": {LagMs: 42, LateForQuorum: &late}}

	got := buildParticipation([]string{"g1a", "g1b"}, "g1prop", false, ts, latency)

	a := got["g1a"]
	if a.PrecommitLagMs == nil || *a.PrecommitLagMs != 42 {
		t.Errorf("g1a PrecommitLagMs = %v, want 42", a.PrecommitLagMs)
	}
	if a.LateForQuorum == nil || !*a.LateForQuorum {
		t.Errorf("g1a LateForQuorum = %v, want true", a.LateForQuorum)
	}
	if b := got["g1b"]; b.PrecommitLagMs != nil || b.LateForQuorum != nil {
		t.Errorf("g1b = %+v, want nil latency", b)
	}
	if p := got["g1prop"]; p.PrecommitLagMs != nil || p.LateForQuorum != nil {
		t.Errorf("proposer-only entry = %+v, want nil latency", p)
	}
}

func TestFetchedBlockParticipation_UsesVotingPower(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	fb := fetchedBlock{
		PrecommitAddrs: []string{"a", "b"},
		Precommits: []signedPrecommit{
			{Addr: "a", Timestamp: t0},
			{Addr: "b", Timestamp: t0.Add(30 * time.Millisecond)},
		},
		ProposerAddr: "a",
		Time:         t0,
	}

	got := fb.participation(map[string]int64{"a": 3, "b": 1})

	if got["b"].PrecommitLagMs == nil || *got["b"].PrecommitLagMs != 30 {
		t.Errorf("b PrecommitLagMs = %v, want 30", got["b"].PrecommitLagMs)
	}
	if got["b"].LateForQuorum == nil || !*got["b"].LateForQuorum {
		t.Errorf("b LateForQuorum = %v, want true (a alone holds 3/4)", got["b"].LateForQuorum)
	}
}
