package gnovalidator

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func bftEntry(addr string, vp int64, signRate float64) database.ValidatorReportEntry {
	return database.ValidatorReportEntry{Addr: addr, VotingPower: vp, SignRate: signRate}
}

func TestComputeBFTMargin(t *testing.T) {
	tests := []struct {
		name          string
		entries       []database.ValidatorReportEntry
		wantAvailable bool
		wantOnline    int
		wantTotal     int
		wantOnlineVP  int64
		wantTotalVP   int64
		wantLosses    int
		wantAbove     bool
		wantMarginPct float64
	}{
		{
			name:          "no entries means no snapshot",
			entries:       nil,
			wantAvailable: false,
		},
		{
			name: "all voting power zero means no snapshot",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 0, 100), bftEntry("g1b", 0, 100),
			},
			wantAvailable: false,
		},
		{
			name: "four equal validators online tolerate one loss",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100),
				bftEntry("g1c", 1, 100), bftEntry("g1d", 1, 100),
			},
			wantAvailable: true, wantOnline: 4, wantTotal: 4, wantOnlineVP: 4, wantTotalVP: 4,
			wantLosses: 1, wantAbove: true, wantMarginPct: 100.0 / 3,
		},
		{
			name: "three equal validators online tolerate no loss",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100), bftEntry("g1c", 1, 100),
			},
			wantAvailable: true, wantOnline: 3, wantTotal: 3, wantOnlineVP: 3, wantTotalVP: 3,
			wantLosses: 0, wantAbove: true, wantMarginPct: 100.0 / 3,
		},
		{
			name: "seven equal validators with one offline tolerate one more loss",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100), bftEntry("g1c", 1, 100),
				bftEntry("g1d", 1, 100), bftEntry("g1e", 1, 100), bftEntry("g1f", 1, 100),
				bftEntry("g1g", 1, 10),
			},
			wantAvailable: true, wantOnline: 6, wantTotal: 7, wantOnlineVP: 6, wantTotalVP: 7,
			wantLosses: 1, wantAbove: true, wantMarginPct: 400.0 / 21,
		},
		{
			name: "unequal voting power removes the largest first",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 10, 100), bftEntry("g1b", 50, 100),
				bftEntry("g1c", 20, 100), bftEntry("g1d", 20, 100),
			},
			wantAvailable: true, wantOnline: 4, wantTotal: 4, wantOnlineVP: 100, wantTotalVP: 100,
			wantLosses: 0, wantAbove: true, wantMarginPct: 100.0 / 3,
		},
		{
			name: "half the voting power offline is below quorum",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100),
				bftEntry("g1c", 1, 0), bftEntry("g1d", 1, 49.9),
			},
			wantAvailable: true, wantOnline: 2, wantTotal: 4, wantOnlineVP: 2, wantTotalVP: 4,
			wantLosses: 0, wantAbove: false, wantMarginPct: -100.0 / 6,
		},
		{
			name: "exactly two thirds online is not above quorum",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100), bftEntry("g1c", 1, 0),
			},
			wantAvailable: true, wantOnline: 2, wantTotal: 3, wantOnlineVP: 2, wantTotalVP: 3,
			wantLosses: 0, wantAbove: false, wantMarginPct: 0,
		},
		{
			name: "sign rate exactly at the threshold counts as online",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 50), bftEntry("g1b", 1, 100),
				bftEntry("g1c", 1, 100), bftEntry("g1d", 1, 100),
			},
			wantAvailable: true, wantOnline: 4, wantTotal: 4, wantOnlineVP: 4, wantTotalVP: 4,
			wantLosses: 1, wantAbove: true, wantMarginPct: 100.0 / 3,
		},
		{
			name: "entries without voting power are ignored",
			entries: []database.ValidatorReportEntry{
				bftEntry("g1a", 1, 100), bftEntry("g1b", 1, 100),
				bftEntry("g1c", 1, 100), bftEntry("g1d", 1, 100),
				bftEntry("g1zero", 0, 0),
			},
			wantAvailable: true, wantOnline: 4, wantTotal: 4, wantOnlineVP: 4, wantTotalVP: 4,
			wantLosses: 1, wantAbove: true, wantMarginPct: 100.0 / 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := computeBFTMargin(tc.entries)
			if m.Available != tc.wantAvailable {
				t.Fatalf("Available = %v, want %v (%+v)", m.Available, tc.wantAvailable, m)
			}
			if !tc.wantAvailable {
				return
			}
			if m.OnlineCount != tc.wantOnline || m.TotalCount != tc.wantTotal {
				t.Errorf("counts = %d/%d, want %d/%d", m.OnlineCount, m.TotalCount, tc.wantOnline, tc.wantTotal)
			}
			if m.OnlineVP != tc.wantOnlineVP || m.TotalVP != tc.wantTotalVP {
				t.Errorf("VP = %d/%d, want %d/%d", m.OnlineVP, m.TotalVP, tc.wantOnlineVP, tc.wantTotalVP)
			}
			if m.TolerableLosses != tc.wantLosses {
				t.Errorf("TolerableLosses = %d, want %d", m.TolerableLosses, tc.wantLosses)
			}
			if m.AboveQuorum() != tc.wantAbove {
				t.Errorf("AboveQuorum = %v, want %v", m.AboveQuorum(), tc.wantAbove)
			}
			if got := m.MarginPercent(); math.Abs(got-tc.wantMarginPct) > 1e-9 {
				t.Errorf("MarginPercent = %v, want %v", got, tc.wantMarginPct)
			}
		})
	}
}

func TestBFTMarginText(t *testing.T) {
	tests := []struct {
		name string
		m    *BFTMargin
		want string
	}{
		{name: "nil margin renders nothing", m: nil, want: ""},
		{
			name: "no snapshot is marked unavailable",
			m:    &BFTMargin{},
			want: "🛡️ BFT margin: unavailable (no voting-power snapshot yet)",
		},
		{
			name: "two or more tolerable losses",
			m:    &BFTMargin{Available: true, OnlineCount: 7, TotalCount: 7, OnlineVP: 7, TotalVP: 7, TolerableLosses: 2},
			want: "🛡️ BFT margin (last 24h): 7/7 validators online (100.0% VP) — 33.3% VP above 2/3 quorum, can lose 2 more validators (largest first) ✅",
		},
		{
			name: "one tolerable loss",
			m:    &BFTMargin{Available: true, OnlineCount: 6, TotalCount: 7, OnlineVP: 6, TotalVP: 7, TolerableLosses: 1},
			want: "🛡️ BFT margin (last 24h): 6/7 validators online (85.7% VP) — 19.0% VP above 2/3 quorum, can lose 1 more validator (largest first) ⚠️",
		},
		{
			name: "no tolerable loss",
			m:    &BFTMargin{Available: true, OnlineCount: 3, TotalCount: 3, OnlineVP: 3, TotalVP: 3, TolerableLosses: 0},
			want: "🛡️ BFT margin (last 24h): 3/3 validators online (100.0% VP) — 33.3% VP above 2/3 quorum, can lose 0 more validators (largest first) 🔴",
		},
		{
			name: "below quorum",
			m:    &BFTMargin{Available: true, OnlineCount: 2, TotalCount: 4, OnlineVP: 2, TotalVP: 4},
			want: "🛡️ BFT margin (last 24h): 2/4 validators online (50.0% VP) — at or below 2/3 quorum 🔴",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bftMarginText(tc.m); got != tc.want {
				t.Fatalf("bftMarginText =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// bftReportData returns a report with one problem validator that proposed
// 3 blocks, and a margin tolerating one more loss.
func bftReportData() DailyReportData {
	return DailyReportData{
		ChainID: "test12", Date: "2025-11-02", TotalCount: 7,
		Problems: []database.ValidatorReportEntry{
			{Addr: "g1bad", Moniker: "bad-mon", Score: 10, Tier: score.TierCritical, MissedBlocks: 400, ProposedBlocks: 3},
		},
		BFTMargin: &BFTMargin{Available: true, OnlineCount: 6, TotalCount: 7, OnlineVP: 6, TotalVP: 7, TolerableLosses: 1},
	}
}

func TestDailyReportRenderers_IncludeBFTMarginAndProposedCount(t *testing.T) {
	d := bftReportData()
	margin := bftMarginText(d.BFTMargin)

	plain := RenderDailyReportPlainText(d)
	if !strings.Contains(plain, margin) {
		t.Errorf("plain text misses the BFT margin line:\n%s", plain)
	}
	if !strings.Contains(plain, "Missed: 400 | Proposed: 3") {
		t.Errorf("plain text misses the proposed count:\n%s", plain)
	}

	embed := RenderDailyReportDiscordEmbed(d)
	if !strings.Contains(embed.Description, margin) {
		t.Errorf("Discord description misses the BFT margin line: %q", embed.Description)
	}
	foundDiscord := false
	for _, f := range embed.Fields {
		if f.Name == "bad-mon" && strings.Contains(f.Value, "Proposed: 3") {
			foundDiscord = true
		}
	}
	if !foundDiscord {
		t.Errorf("Discord fields miss the proposed count: %+v", embed.Fields)
	}

	var slackMargin, slackProposed bool
	for _, b := range RenderDailyReportSlackBlocks(d) {
		if b.Text == nil {
			continue
		}
		if b.Text.Text == margin {
			slackMargin = true
		}
		if strings.Contains(b.Text.Text, "bad-mon") && strings.Contains(b.Text.Text, "Proposed: 3") {
			slackProposed = true
		}
	}
	if !slackMargin || !slackProposed {
		t.Errorf("Slack blocks miss the margin (%v) or the proposed count (%v)", slackMargin, slackProposed)
	}

	text, _, _ := RenderDailyReportTelegramHTML(d)
	if !strings.Contains(text, html.EscapeString(margin)) {
		t.Errorf("Telegram text misses the BFT margin line:\n%s", text)
	}
	if !strings.Contains(text, "Missed: 400 | Proposed: 3") {
		t.Errorf("Telegram text misses the proposed count:\n%s", text)
	}
}

func TestDailyReportRenderers_UnavailableMarginWithoutSnapshot(t *testing.T) {
	d := bftReportData()
	d.BFTMargin = &BFTMargin{}
	want := "BFT margin: unavailable (no voting-power snapshot yet)"

	if got := RenderDailyReportPlainText(d); !strings.Contains(got, want) {
		t.Errorf("plain text:\n%s", got)
	}
	if got := RenderDailyReportDiscordEmbed(d).Description; !strings.Contains(got, want) {
		t.Errorf("Discord description: %q", got)
	}
	found := false
	for _, b := range RenderDailyReportSlackBlocks(d) {
		if b.Text != nil && strings.Contains(b.Text.Text, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("Slack blocks miss the unavailable margin line")
	}
	if got, _, _ := RenderDailyReportTelegramHTML(d); !strings.Contains(got, want) {
		t.Errorf("Telegram text:\n%s", got)
	}
}

func TestDailyReportRenderers_OmitMarginWhenNotComputed(t *testing.T) {
	d := bftReportData()
	d.BFTMargin = nil

	if got := RenderDailyReportPlainText(d); strings.Contains(got, "BFT margin") {
		t.Errorf("plain text must omit the margin line:\n%s", got)
	}
	if got := RenderDailyReportDiscordEmbed(d).Description; strings.Contains(got, "BFT margin") {
		t.Errorf("Discord description must omit the margin line: %q", got)
	}
	for _, b := range RenderDailyReportSlackBlocks(d) {
		if b.Text != nil && strings.Contains(b.Text.Text, "BFT margin") {
			t.Errorf("Slack blocks must omit the margin line")
		}
	}
	if got, _, _ := RenderDailyReportTelegramHTML(d); strings.Contains(got, "BFT margin") {
		t.Errorf("Telegram text must omit the margin line:\n%s", got)
	}
}

func TestBuildDailyReportData_BFTMarginWithoutSnapshot(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// Participation exists but no addr_monikers voting power: no snapshot yet.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1a", Moniker: "a-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})

	data, err := BuildDailyReportData(db, "test12", "2025-11-02", ChainHealthSnapshot{}, 1, 100)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}
	if data.BFTMargin == nil {
		t.Fatalf("BFTMargin must be set by BuildDailyReportData")
	}
	if data.BFTMargin.Available {
		t.Fatalf("BFTMargin.Available = true without a VP snapshot: %+v", *data.BFTMargin)
	}
}

func TestBuildDailyReportData_BFTMarginAndProposedCount(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// g1online: signs every block -> online, VP 5.
	for h := int64(1); h <= 4; h++ {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: "g1online", Moniker: "online-mon",
			BlockHeight: h, Date: now, Participated: true,
		})
	}
	// g1weak: signs 1 of 4 blocks (25%) and proposed it -> offline for the
	// margin, Critical in Problems with ProposedBlocks 1.
	for h := int64(1); h <= 4; h++ {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: "g1weak", Moniker: "weak-mon",
			BlockHeight: h, Date: now, Participated: h == 1, Proposed: h == 1,
		})
	}
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1online", Moniker: "online-mon", VotingPower: 5})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1weak", Moniker: "weak-mon", VotingPower: 2})

	data, err := BuildDailyReportData(db, "test12", "2025-11-02", ChainHealthSnapshot{}, 1, 100)
	if err != nil {
		t.Fatalf("BuildDailyReportData error: %v", err)
	}
	m := data.BFTMargin
	if m == nil || !m.Available {
		t.Fatalf("BFTMargin must be available with a VP snapshot, got %+v", m)
	}
	if m.OnlineCount != 1 || m.TotalCount != 2 || m.OnlineVP != 5 || m.TotalVP != 7 {
		t.Fatalf("BFTMargin = %+v, want 1/2 online, VP 5/7", *m)
	}

	var weak *database.ValidatorReportEntry
	for i := range data.Problems {
		if data.Problems[i].Addr == "g1weak" {
			weak = &data.Problems[i]
		}
	}
	if weak == nil {
		t.Fatalf("g1weak must appear in Problems, got: %+v", data.Problems)
	}
	if weak.ProposedBlocks != 1 {
		t.Fatalf("g1weak ProposedBlocks = %d, want 1", weak.ProposedBlocks)
	}
}

// TestRenderDailyReportTelegramHTML_WorstCaseFitsMessageLimit pins
// maxProblemsTelegram: with long monikers, large counters, a chain summary
// and the BFT margin line, the visible text (tags stripped, entities
// decoded, counted in UTF-16 units as Telegram does) stays within the
// 4096-char message limit.
func TestRenderDailyReportTelegramHTML_WorstCaseFitsMessageLimit(t *testing.T) {
	d := bftReportData()
	d.ChainSummary = "🟢 Block #12345678 (59m ago) — Consensus: round 12 — Slow\nNetwork: 120 peers | Mempool: 99999 pending txs"
	d.BlockRangeStart, d.BlockRangeEnd = 12000000, 12345678
	d.ReportLink = "https://example.com/reports/test12"
	d.Problems = nil
	for i := 0; i < 200; i++ {
		d.Problems = append(d.Problems, database.ValidatorReportEntry{
			Addr:    "g1" + strings.Repeat("x", 38),
			Moniker: fmt.Sprintf("%s-%03d", strings.Repeat("m", 28), i),
			Score:   100, Tier: score.TierCritical, MissedBlocks: 1234567, ProposedBlocks: 123456,
		})
	}
	text, _, _ := RenderDailyReportTelegramHTML(d)
	visible := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, ""))
	if n := len(utf16.Encode([]rune(visible))); n > 4096 {
		t.Fatalf("worst-case Telegram report is %d UTF-16 units, over the 4096 limit", n)
	}
}

// TestRenderDailyReportDiscordEmbed_ReportLinkIsClickable pins that the
// report link is never put in the footer, which Discord renders as plain
// text: it goes on the title URL and in a last Markdown-link field.
func TestRenderDailyReportDiscordEmbed_ReportLinkIsClickable(t *testing.T) {
	d := bftReportData()
	d.ReportLink = "https://example.com/reports/(test12)"
	embed := RenderDailyReportDiscordEmbed(d)

	if embed.Footer != nil {
		t.Fatalf("report link must not go in the footer: %+v", embed.Footer)
	}
	if embed.URL != d.ReportLink {
		t.Fatalf("embed.URL = %q, want %q", embed.URL, d.ReportLink)
	}
	last := embed.Fields[len(embed.Fields)-1]
	want := "📊 [Open the validator report](https://example.com/reports/%28test12%29)"
	if last.Name != "Full report" || last.Value != want {
		t.Fatalf("last field = %+v, want Full report / %q", last, want)
	}

	d.ReportLink = ""
	embed = RenderDailyReportDiscordEmbed(d)
	if embed.URL != "" || embed.Fields[len(embed.Fields)-1].Name == "Full report" {
		t.Fatalf("no report link must give no title URL and no link field: %+v", embed)
	}
}
