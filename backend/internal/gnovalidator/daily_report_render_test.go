package gnovalidator

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestBuildDailyReportData_PartitionsProblemsByTier(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// g1healthy: perfect participation, no alerts -> Tier Good/Excellent, not a problem.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 5})

	// g1critical: no participation rows in last_24h at all -> score 0/Critical (documented behavior).
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1critical", Moniker: "critical-mon", VotingPower: 2})

	snap := ChainHealthSnapshot{RPCReachable: false}
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1, 1)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}

	if data.TotalCount != 2 {
		t.Fatalf("TotalCount = %d, want 2", data.TotalCount)
	}
	if data.AllHealthy {
		t.Fatalf("AllHealthy = true, want false (g1critical must be flagged)")
	}
	found := false
	for _, p := range data.Problems {
		if p.Addr == "g1critical" {
			found = true
		}
		if p.Addr == "g1healthy" {
			t.Fatalf("g1healthy must not appear in Problems")
		}
	}
	if !found {
		t.Fatalf("g1critical must appear in Problems, got: %+v", data.Problems)
	}
}

func TestBuildDailyReportData_AllHealthyWhenNoProblems(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 5})

	snap := ChainHealthSnapshot{RPCReachable: false}
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1, 1)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}
	if !data.AllHealthy {
		t.Fatalf("AllHealthy = false, want true, Problems: %+v", data.Problems)
	}
	if len(data.Problems) != 0 {
		t.Fatalf("Problems must be empty when AllHealthy, got: %+v", data.Problems)
	}
}
