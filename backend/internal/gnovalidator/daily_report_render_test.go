package gnovalidator

import (
	"fmt"
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
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1)
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
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1)
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

func TestBuildDailyReportData_TruncatesBeyondMaxProblemsShown(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Seed more valset members than maxProblemsShown, each with a voting-power
	// row but zero participation history (score 0/Critical, same shape as the
	// documented g1critical case), so all of them land in Problems.
	const seeded = maxProblemsShown + 5
	for i := 0; i < seeded; i++ {
		addr := fmt.Sprintf("g1critical%02d", i)
		db.Create(&database.AddrMoniker{ChainID: "test12", Addr: addr, Moniker: "", VotingPower: 1})
	}

	snap := ChainHealthSnapshot{RPCReachable: false}
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}

	if len(data.Problems) != maxProblemsShown {
		t.Fatalf("len(Problems) = %d, want %d", len(data.Problems), maxProblemsShown)
	}
	if data.TruncatedCount != seeded-maxProblemsShown {
		t.Fatalf("TruncatedCount = %d, want %d", data.TruncatedCount, seeded-maxProblemsShown)
	}
	if data.AllHealthy {
		t.Fatalf("AllHealthy = true, want false when Problems is non-empty")
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

func TestRenderDailyReportSlackBlocks_OneSectionPerProblem(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 2,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
			{Addr: "g2", Moniker: "m2", Score: 40, Tier: score.TierWatch, MissedBlocks: 2},
		},
		ReportLink: "https://example.com/reports/test12",
	}
	blocks := RenderDailyReportSlackBlocks(d)

	sectionCount := 0
	for _, b := range blocks {
		if b.Type == "section" && b.Text != nil && strings.Contains(b.Text.Text, "m1") {
			sectionCount++
		}
	}
	if sectionCount == 0 {
		t.Fatalf("expected a section block mentioning m1, got: %+v", blocks)
	}
	lastBlock := blocks[len(blocks)-1]
	if lastBlock.Type != "context" {
		t.Fatalf("expected the last block to be a context block with the report link, got: %+v", lastBlock)
	}
}

func TestRenderDailyReportTelegramHTML_EscapesMonikerAndSetsButton(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1", Moniker: "<script>", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
		},
		ReportLink: "https://example.com/reports/test12",
	}
	text, buttonText, buttonURL := RenderDailyReportTelegramHTML(d)

	if strings.Contains(text, "<script>") {
		t.Fatalf("moniker must be HTML-escaped, got:\n%s", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("expected escaped moniker in output, got:\n%s", text)
	}
	if buttonURL != "https://example.com/reports/test12" {
		t.Fatalf("buttonURL = %q, want the report link", buttonURL)
	}
	if buttonText == "" {
		t.Fatalf("buttonText must not be empty when a button is shown")
	}
}

func TestRenderDailyReportTelegramHTML_NoButtonWhenNoReportLink(t *testing.T) {
	d := DailyReportData{ChainID: "test12", Date: "2025-11-02", TotalCount: 3, AllHealthy: true}
	_, _, buttonURL := RenderDailyReportTelegramHTML(d)
	if buttonURL != "" {
		t.Fatalf("buttonURL = %q, want empty when ReportLink is unset", buttonURL)
	}
}

// emptyMonikerProblem builds a problem-validator fixture with no known
// moniker (e.g. a newly-joined valset member with no participation history
// yet), matching the g1critical shape from BuildDailyReportData tests above.
func emptyMonikerProblem() database.ValidatorReportEntry {
	return database.ValidatorReportEntry{
		Addr: "g1newvalidatorwithnomoniker", Moniker: "",
		Score: 0, Tier: score.TierCritical, MissedBlocks: 100,
	}
}

func TestRenderDailyReportPlainText_EmptyMonikerFallsBackToAddr(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	got := RenderDailyReportPlainText(d)
	if !strings.Contains(got, p.Addr) {
		t.Fatalf("expected the address to appear as a fallback display name, got:\n%s", got)
	}
}

func TestRenderDailyReportDiscordEmbed_EmptyMonikerFallsBackToAddr(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	embed := RenderDailyReportDiscordEmbed(d)
	if len(embed.Fields) != 1 {
		t.Fatalf("expected one field for the problem validator, got: %+v", embed.Fields)
	}
	if embed.Fields[0].Name == "" {
		t.Fatalf("Discord embed field Name must never be empty (Discord rejects the whole request), got: %+v", embed.Fields[0])
	}
	if embed.Fields[0].Name != p.Addr {
		t.Fatalf("Fields[0].Name = %q, want fallback to Addr %q", embed.Fields[0].Name, p.Addr)
	}
}

func TestRenderDailyReportSlackBlocks_EmptyMonikerFallsBackToAddr(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	blocks := RenderDailyReportSlackBlocks(d)

	found := false
	for _, b := range blocks {
		if b.Type == "section" && b.Text != nil && strings.Contains(b.Text.Text, p.Addr) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a section block mentioning the fallback address %q, got: %+v", p.Addr, blocks)
	}
}

func TestRenderDailyReportTelegramHTML_EmptyMonikerFallsBackToAddr(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	text, _, _ := RenderDailyReportTelegramHTML(d)
	if !strings.Contains(text, p.Addr) {
		t.Fatalf("expected the address to appear as a fallback display name, got:\n%s", text)
	}
	// The rendered <b>...</b> display-name segment must not be left blank.
	if strings.Contains(text, "<b></b>") {
		t.Fatalf("display-name segment must not be empty, got:\n%s", text)
	}
}

func TestRenderDailyReportPlainText_TruncatedCountAddsSummaryLine(t *testing.T) {
	base := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 30,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
		},
		ReportLink: "https://example.com/reports/test12",
	}

	withTruncation := base
	withTruncation.TruncatedCount = 7
	got := RenderDailyReportPlainText(withTruncation)
	if !strings.Contains(got, "7 more") {
		t.Fatalf("expected a '+N more' summary line when TruncatedCount > 0, got:\n%s", got)
	}

	noTruncation := base
	noTruncation.TruncatedCount = 0
	got = RenderDailyReportPlainText(noTruncation)
	if strings.Contains(got, "more") {
		t.Fatalf("did not expect a '+N more' summary line when TruncatedCount == 0, got:\n%s", got)
	}
}

func TestRenderDailyReportDiscordEmbed_TruncatedCountAddsField(t *testing.T) {
	base := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 30,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
		},
	}

	withTruncation := base
	withTruncation.TruncatedCount = 7
	embed := RenderDailyReportDiscordEmbed(withTruncation)
	if len(embed.Fields) != 2 {
		t.Fatalf("expected an extra field summarizing truncated problems, got: %+v", embed.Fields)
	}
	if !strings.Contains(embed.Fields[len(embed.Fields)-1].Value, "7 more") {
		t.Fatalf("expected the last field to mention the truncated count, got: %+v", embed.Fields[len(embed.Fields)-1])
	}

	noTruncation := base
	noTruncation.TruncatedCount = 0
	embed = RenderDailyReportDiscordEmbed(noTruncation)
	if len(embed.Fields) != 1 {
		t.Fatalf("did not expect an extra field when TruncatedCount == 0, got: %+v", embed.Fields)
	}
}
