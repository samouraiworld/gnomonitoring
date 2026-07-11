package gnovalidator

import (
	"testing"
	"time"
)

func TestBuildParticipation_ProposerCreditedWithoutPrecommit(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// proposer "g1prop" did not precommit the previous block (e.g. it just
	// came back online and was immediately selected to propose).
	got := buildParticipation([]string{"g1a", "g1b"}, "g1prop", false, ts)

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
	got := buildParticipation([]string{"g1prop", "g1b"}, "g1prop", true, ts)

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
	got := buildParticipation([]string{"g1a"}, "g1prop", true, ts)

	if p := got["g1a"]; !p.Participated || p.Proposed || p.TxContribution {
		t.Errorf("signer entry = %+v, want Participated=true, Proposed=false, TxContribution=false", p)
	}
}

func TestBuildParticipation_EmptyPrecommits(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	got := buildParticipation(nil, "g1prop", false, ts)

	if len(got) != 1 {
		t.Fatalf("want exactly the proposer entry, got %+v", got)
	}
	if p := got["g1prop"]; !p.Proposed || p.Participated {
		t.Errorf("proposer entry = %+v, want Proposed=true, Participated=false", p)
	}
}
