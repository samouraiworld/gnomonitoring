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
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1, 100)
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
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1, 100)
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

// TestBuildDailyReportData_KeepsFullUntruncatedProblemsList pins the fix
// that BuildDailyReportData no longer truncates Problems itself: truncation
// is now per-channel (see truncateProblems), applied independently by each
// renderer at its own limit, since Discord/Slack/Telegram have very
// different real transport limits.
func TestBuildDailyReportData_KeepsFullUntruncatedProblemsList(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Seed more valset members than the tightest channel limit
	// (maxProblemsDiscord), each with a voting-power row but zero
	// participation history (score 0/Critical, same shape as the documented
	// g1critical case), so all of them land in Problems.
	const seeded = maxProblemsDiscord + 5
	for i := 0; i < seeded; i++ {
		addr := fmt.Sprintf("g1critical%02d", i)
		db.Create(&database.AddrMoniker{ChainID: "test12", Addr: addr, Moniker: "", VotingPower: 1})
	}

	snap := ChainHealthSnapshot{RPCReachable: false}
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 1, 100)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}

	if len(data.Problems) != seeded {
		t.Fatalf("len(Problems) = %d, want %d (full, untruncated list)", len(data.Problems), seeded)
	}
	if data.AllHealthy {
		t.Fatalf("AllHealthy = true, want false when Problems is non-empty")
	}
}

func TestTruncateProblems(t *testing.T) {
	all := make([]database.ValidatorReportEntry, 25)
	for i := range all {
		all[i] = database.ValidatorReportEntry{Addr: fmt.Sprintf("g1%02d", i)}
	}

	shown, truncatedCount := truncateProblems(all, 20)
	if len(shown) != 20 || truncatedCount != 5 {
		t.Fatalf("truncateProblems(25 items, limit 20) = (%d shown, %d truncated), want (20, 5)", len(shown), truncatedCount)
	}

	shown, truncatedCount = truncateProblems(all, 30)
	if len(shown) != 25 || truncatedCount != 0 {
		t.Fatalf("truncateProblems(25 items, limit 30) = (%d shown, %d truncated), want (25, 0)", len(shown), truncatedCount)
	}
}

func TestBuildDailyReportData_SetsBlockRangeFromCalculateRateWindow(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 5})

	snap := ChainHealthSnapshot{RPCReachable: false}
	data, err := BuildDailyReportData(db, "test12", "2025-11-02", snap, 850990, 851134)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}
	if data.BlockRangeStart != 850990 || data.BlockRangeEnd != 851134 {
		t.Fatalf("BlockRange = [%d,%d], want [850990,851134]", data.BlockRangeStart, data.BlockRangeEnd)
	}
}

func TestRenderDailyReportPlainText_ReportWindow(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1, AllHealthy: true,
		BlockRangeStart: 850990, BlockRangeEnd: 851134,
	}
	got := RenderDailyReportPlainText(d)
	if !strings.Contains(got, "Report window: blocks 850990–851134") {
		t.Fatalf("expected a report-window line, got:\n%s", got)
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
		"✅ All 3 validators healthy (last 24h)\n"
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
	if len(embed.Fields) != 2 || embed.Fields[0].Name != "Status" || embed.Fields[1].Name != "m1" {
		t.Fatalf("expected a Status field followed by one field for the problem validator, got: %+v", embed.Fields)
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

// TestRenderDailyReportPlainText_EmptyMonikerFallsBackToUnknown pins the
// codebase-wide "unknown" sentinel convention (see isResolvedMoniker in
// valoper.go, FormatMissedBlocksLast24h in health.go): an unresolved moniker
// must render as "unknown", not the raw address, so it's recognizable as
// "still needs a moniker" rather than mistaken for a resolved display name.
func TestRenderDailyReportPlainText_EmptyMonikerFallsBackToUnknown(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	got := RenderDailyReportPlainText(d)
	if !strings.Contains(got, "unknown ("+p.Addr+")") {
		t.Fatalf("expected \"unknown (%s)\" as the display name, got:\n%s", p.Addr, got)
	}
}

func TestRenderDailyReportDiscordEmbed_EmptyMonikerFallsBackToUnknown(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	embed := RenderDailyReportDiscordEmbed(d)
	if len(embed.Fields) != 2 {
		t.Fatalf("expected a Status field plus one field for the problem validator, got: %+v", embed.Fields)
	}
	if embed.Fields[1].Name == "" {
		t.Fatalf("Discord embed field Name must never be empty (Discord rejects the whole request), got: %+v", embed.Fields[1])
	}
	if embed.Fields[1].Name != "unknown" {
		t.Fatalf("Fields[1].Name = %q, want fallback to \"unknown\"", embed.Fields[1].Name)
	}
}

func TestRenderDailyReportSlackBlocks_EmptyMonikerFallsBackToUnknown(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	blocks := RenderDailyReportSlackBlocks(d)

	found := false
	for _, b := range blocks {
		if b.Type == "section" && b.Text != nil && strings.Contains(b.Text.Text, "*unknown*") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a section block with \"*unknown*\" as the display name, got: %+v", blocks)
	}
}

func TestRenderDailyReportTelegramHTML_EmptyMonikerFallsBackToUnknown(t *testing.T) {
	p := emptyMonikerProblem()
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 1,
		Problems: []database.ValidatorReportEntry{p},
	}
	text, _, _ := RenderDailyReportTelegramHTML(d)
	if !strings.Contains(text, "<b>unknown</b>") {
		t.Fatalf("expected \"<b>unknown</b>\" as the display name, got:\n%s", text)
	}
}

// manyProblems builds n distinct Watch/Critical problem-validator fixtures,
// used to exercise per-channel truncation (see truncateProblems).
func manyProblems(n int) []database.ValidatorReportEntry {
	problems := make([]database.ValidatorReportEntry, n)
	for i := range problems {
		problems[i] = database.ValidatorReportEntry{
			Addr: fmt.Sprintf("g1%03d", i), Moniker: fmt.Sprintf("m%03d", i),
			Score: 10, Tier: score.TierCritical, MissedBlocks: 5,
		}
	}
	return problems
}

func TestRenderDailyReportPlainText_TruncatedCountAddsSummaryLine(t *testing.T) {
	base := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 30,
		ReportLink: "https://example.com/reports/test12",
	}

	withTruncation := base
	withTruncation.Problems = manyProblems(maxProblemsPlainText + 7)
	got := RenderDailyReportPlainText(withTruncation)
	if !strings.Contains(got, "7 more") {
		t.Fatalf("expected a '+N more' summary line when Problems exceeds maxProblemsPlainText, got:\n%s", got)
	}

	noTruncation := base
	noTruncation.Problems = []database.ValidatorReportEntry{
		{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
	}
	got = RenderDailyReportPlainText(noTruncation)
	if strings.Contains(got, "more") {
		t.Fatalf("did not expect a '+N more' summary line when Problems is within the limit, got:\n%s", got)
	}
}

// TestReportLinkAppearsExactlyOnceWhenTruncated is a regression test: the
// report link used to appear both inline in the "…and N more" text and again
// in each renderer's own footer/context block/trailing line/button. Seeds
// more problems than the highest per-channel limit (maxProblemsPlainText/
// maxProblemsTelegram, both 60) so every renderer truncates.
func TestReportLinkAppearsExactlyOnceWhenTruncated(t *testing.T) {
	d := DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 90,
		Problems:   manyProblems(maxProblemsPlainText + 7),
		ReportLink: "https://example.com/reports/test12",
	}

	plain := RenderDailyReportPlainText(d)
	if n := strings.Count(plain, d.ReportLink); n != 1 {
		t.Fatalf("plain text: report link appeared %d times, want 1:\n%s", n, plain)
	}

	embed := RenderDailyReportDiscordEmbed(d)
	discordText := embed.Description
	for _, f := range embed.Fields {
		discordText += f.Name + f.Value
	}
	if embed.Footer != nil {
		discordText += embed.Footer.Text
	}
	if n := strings.Count(discordText, d.ReportLink); n != 1 {
		t.Fatalf("discord embed: report link appeared %d times, want 1: %+v", n, embed)
	}

	slackBlocks := RenderDailyReportSlackBlocks(d)
	var slackText string
	for _, b := range slackBlocks {
		if b.Text != nil {
			slackText += b.Text.Text
		}
		for _, e := range b.Elements {
			slackText += e.Text
		}
	}
	if n := strings.Count(slackText, d.ReportLink); n != 1 {
		t.Fatalf("slack blocks: report link appeared %d times, want 1: %+v", n, slackBlocks)
	}

	text, _, buttonURL := RenderDailyReportTelegramHTML(d)
	if buttonURL != d.ReportLink {
		t.Fatalf("telegram: buttonURL = %q, want %q", buttonURL, d.ReportLink)
	}
	if strings.Contains(text, d.ReportLink) {
		t.Fatalf("telegram: report link must appear only via the button, not in the message body:\n%s", text)
	}
}

func TestRenderDailyReportDiscordEmbed_TruncatedCountAddsField(t *testing.T) {
	base := DailyReportData{ChainID: "test12", Date: "2025-11-02", TotalCount: 30}

	withTruncation := base
	withTruncation.Problems = manyProblems(maxProblemsDiscord + 7)
	embed := RenderDailyReportDiscordEmbed(withTruncation)
	if len(embed.Fields) != maxProblemsDiscord+2 {
		t.Fatalf("expected a Status field, %d problem fields, plus one truncation-summary field, got: %d fields", maxProblemsDiscord, len(embed.Fields))
	}
	if !strings.Contains(embed.Fields[len(embed.Fields)-1].Value, "7 more") {
		t.Fatalf("expected the last field to mention the truncated count, got: %+v", embed.Fields[len(embed.Fields)-1])
	}

	noTruncation := base
	noTruncation.Problems = []database.ValidatorReportEntry{
		{Addr: "g1", Moniker: "m1", Score: 10, Tier: score.TierCritical, MissedBlocks: 5},
	}
	embed = RenderDailyReportDiscordEmbed(noTruncation)
	if len(embed.Fields) != 2 {
		t.Fatalf("did not expect an extra field when Problems is within the limit, got: %+v", embed.Fields)
	}
}
