package gnovalidator

import (
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
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

func TestRenderDailyReportPlainText_AllHealthy(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02",
		ChainSummary: "🟢 Block #100 (5s ago) — Consensus: round 0 — Normal",
		TotalCount:   3, AllHealthy: true,
	}
	got := RenderDailyReportPlainText(d)
	want := "📊 [test12] Daily Summary — 2025-11-02\n" +
		"🟢 Block #100 (5s ago) — Consensus: round 0 — Normal\n" +
		"✅ All 3 validators healthy\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderDailyReportPlainText_WithProblems(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02",
		TotalCount: 2, AllHealthy: false,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1bad", Moniker: "bad-mon", Score: 10, Tier: score.TierCritical, MissedBlocks: 400},
		},
	}
	got := RenderDailyReportPlainText(d)
	if !strings.Contains(got, "bad-mon") || !strings.Contains(got, "Critical") || !strings.Contains(got, "10") {
		t.Fatalf("expected problem validator details in output, got:\n%s", got)
	}
}

func TestRenderDailyReportDiscordEmbed_ColorReflectsWorstTier(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
		},
	}
	embed := RenderDailyReportDiscordEmbed(d)
	if embed.Color != 0xE74C3C {
		t.Fatalf("Color = %#x, want %#x (critical red)", embed.Color, 0xE74C3C)
	}
	if len(embed.Fields) != 1 || embed.Fields[0].Name != "m1" {
		t.Fatalf("expected one field for the problem validator, got: %+v", embed.Fields)
	}
}

func TestRenderDailyReportDiscordEmbed_HealthyIsGreenWithSingleField(t *testing.T) {
	d := DailyReportData{ChainID: "test12", Date: "2025-11-02", TotalCount: 4, AllHealthy: true}
	embed := RenderDailyReportDiscordEmbed(d)
	if embed.Color != 0x2ECC71 {
		t.Fatalf("Color = %#x, want %#x (healthy green)", embed.Color, 0x2ECC71)
	}
	if len(embed.Fields) != 1 {
		t.Fatalf("expected a single status field, got: %+v", embed.Fields)
	}
}
