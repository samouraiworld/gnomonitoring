package gnovalidator

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"gorm.io/gorm"
)

// Per-channel caps on how many problem validators a renderer includes
// inline. Without a cap, a chain-wide incident (halt, mass downtime) can push
// every validator into Problems at once, and the resulting payload can
// exceed a channel's transport limit — Discord's 25-field embed limit,
// Slack's 50-block message limit, or Telegram's 4096-char message limit —
// causing a delivery failure exactly when the report matters most. Each
// renderer truncates DailyReportData.Problems (always the full, untruncated
// list) to its own limit at render time, rather than sharing one flat cap:
// the channels' real limits differ by roughly 3x between the tightest
// (Discord) and the most generous (Telegram), so a single shared cap either
// truncates Telegram/Slack earlier than necessary or risks Discord going
// over its field limit.
const (
	maxProblemsDiscord   = 20 // Discord embeds cap at 25 fields; leaves room for a "…and N more" field.
	maxProblemsSlack     = 40 // Slack messages cap at 50 blocks; leaves room for header/summary/divider/link blocks.
	maxProblemsTelegram  = 60 // Telegram caps messages at 4096 chars; ~60 one-line entries comfortably fits alongside the header/summary text.
	maxProblemsPlainText = 60 // Server log line, no hard transport limit; matches Telegram's budget for a readable trace.
)

// DailyReportData is the channel-neutral summary of one chain's daily
// report. Each channel renderer (plain text, Discord embed, Slack Block Kit,
// Telegram HTML) consumes the same DailyReportData built once per report,
// and independently truncates Problems to its own channel limit.
type DailyReportData struct {
	ChainID       string
	Date          string
	ChainSummary  string
	// BlockRangeStart/BlockRangeEnd are the first/last block heights covered
	// by this report's participation data (yesterday's UTC calendar day, per
	// CalculateRate) — the window the Score/Missed figures below are computed
	// over. Distinct from ChainSummary's "Block #N" line, which reports the
	// chain's live tip at snapshot-fetch time, not the reported period.
	BlockRangeStart int64
	BlockRangeEnd   int64
	ValsetChanges   []ValsetChange
	// Problems holds every validator in tier Watch or Critical for the
	// last_24h period, sorted worst (lowest score) first — the full list,
	// never truncated here. Empty when AllHealthy. Each renderer calls
	// truncateProblems with its own channel-specific limit.
	Problems   []database.ValidatorReportEntry
	TotalCount int
	AllHealthy bool
	ReportLink string
}

// truncateProblems caps all to at most limit entries, returning the shown
// slice and how many entries were left out.
func truncateProblems(all []database.ValidatorReportEntry, limit int) (shown []database.ValidatorReportEntry, truncatedCount int) {
	if len(all) <= limit {
		return all, 0
	}
	return all[:limit], len(all) - limit
}

// reportWindowText returns the plain (unescaped) "Report window: blocks
// X–Y" line describing the block-height range this report's Score/Missed
// figures are computed over, or "" when the range is unset (e.g. a
// DailyReportData built directly in a test without going through
// BuildDailyReportData).
func reportWindowText(d DailyReportData) string {
	if d.BlockRangeStart == 0 && d.BlockRangeEnd == 0 {
		return ""
	}
	return fmt.Sprintf("Report window: blocks %d–%d", d.BlockRangeStart, d.BlockRangeEnd)
}

// truncatedSummaryText returns the plain (unescaped, unformatted) "N more"
// summary line for a channel renderer that truncated Problems via
// truncateProblems. Callers embed it in their channel-specific formatting
// (plain text line, Discord field value, Slack block text, Telegram HTML).
// Only call when truncatedCount > 0. It deliberately does not repeat
// d.ReportLink: every renderer already appends the link once of its own
// (footer/context block/trailing line/button) whenever ReportLink is set, so
// including it here too would render it twice in the same message.
func truncatedSummaryText(truncatedCount int) string {
	return fmt.Sprintf("…and %d more", truncatedCount)
}

// BuildDailyReportData assembles a DailyReportData for chainID's healthy-chain
// daily report. snap is the already-fetched ChainHealthSnapshot. minBlock and
// maxBlock are the first/last block heights CalculateRate found for the
// reported UTC calendar day; they bound both the valset-changes window and
// the displayed report interval. Note this is a different window than
// FormatHealthyReport's own (unused) minBlock/maxBlock parameters, which are
// dead code there — that function actually filters valset changes on
// snap.MinBlock, a rolling-last-24h value from FetchChainHealthSnapshot.
func BuildDailyReportData(db *gorm.DB, chainID, date string, snap ChainHealthSnapshot, minBlock, maxBlock int64) (DailyReportData, error) {
	ctx, err := database.LoadValidatorReportContext(db, chainID)
	if err != nil {
		return DailyReportData{}, err
	}
	entries, err := database.BuildChainValidatorReport(db, ctx, chainID, "last_24h", "")
	if err != nil {
		return DailyReportData{}, err
	}

	var problems []database.ValidatorReportEntry
	for _, e := range entries {
		if e.Tier == score.TierWatch || e.Tier == score.TierCritical {
			problems = append(problems, e)
		}
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Score != problems[j].Score {
			return problems[i].Score < problems[j].Score
		}
		return problems[i].Addr < problems[j].Addr
	})

	var chainSummary string
	if snap.RPCReachable {
		emoji := chainStatusEmoji(snap)
		blockAge := formatBlockAge(snap.LatestBlockTime)
		chainSummary = fmt.Sprintf("%s Block #%d (%s) — Consensus: round %d — %s",
			emoji, snap.LatestBlockHeight, blockAge,
			snap.ConsensusRound, consensusLabel(snap.ConsensusRound))
		if snap.PeerCount > 0 || snap.MempoolTxCount > 0 {
			chainSummary += fmt.Sprintf("\nNetwork: %d peers | Mempool: %d pending txs", snap.PeerCount, snap.MempoolTxCount)
		}
	}

	var recentChanges []ValsetChange
	for _, vc := range snap.ValsetChanges {
		if vc.BlockNum >= minBlock {
			recentChanges = append(recentChanges, vc)
		}
	}

	return DailyReportData{
		ChainID:         chainID,
		Date:            date,
		ChainSummary:    chainSummary,
		BlockRangeStart: minBlock,
		BlockRangeEnd:   maxBlock,
		ValsetChanges:   recentChanges,
		Problems:        problems,
		TotalCount:      len(entries),
		AllHealthy:      len(problems) == 0,
		ReportLink:      reportLinkURL(db, chainID),
	}, nil
}

// RenderDailyReportPlainText formats DailyReportData as a plain-text report.
// Used for logging and as the textual basis for channels without a richer
// renderer.
func RenderDailyReportPlainText(d DailyReportData) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n", d.ChainID, d.Date))
	if d.ChainSummary != "" {
		sb.WriteString(d.ChainSummary + "\n")
	}
	if w := reportWindowText(d); w != "" {
		sb.WriteString(w + "\n")
	}
	if d.AllHealthy {
		sb.WriteString(fmt.Sprintf("✅ All %d validators healthy\n", d.TotalCount))
	} else {
		shown, truncatedCount := truncateProblems(d.Problems, maxProblemsPlainText)
		sb.WriteString(fmt.Sprintf("⚠️ %d/%d validators need attention:\n", len(d.Problems), d.TotalCount))
		for _, p := range shown {
			sb.WriteString(fmt.Sprintf("  %s (%s) — Tier: %s | Score: %d | Missed: %d\n",
				p.DisplayName(), p.Addr, p.Tier, p.Score, p.MissedBlocks))
		}
		if truncatedCount > 0 {
			sb.WriteString("  " + truncatedSummaryText(truncatedCount) + "\n")
		}
	}
	for _, vc := range d.ValsetChanges {
		if vc.NewPower == 0 {
			sb.WriteString(fmt.Sprintf("Valset: block #%d — %s removed\n", vc.BlockNum, vc.Address))
		} else {
			sb.WriteString(fmt.Sprintf("Valset: block #%d — %s added (power: %d)\n", vc.BlockNum, vc.Address, vc.NewPower))
		}
	}
	if d.ReportLink != "" {
		sb.WriteString("📊 Full report: " + d.ReportLink + "\n")
	}
	return sb.String()
}

const (
	discordColorCritical = 0xE74C3C
	discordColorWarning  = 0xF39C12
	discordColorHealthy  = 0x2ECC71
)

// RenderDailyReportDiscordEmbed builds a single Discord embed summarizing
// the daily report: color reflects the worst tier among Problems (red for
// any Critical, orange for Watch-only, green when AllHealthy), with one
// field per problem validator.
func RenderDailyReportDiscordEmbed(d DailyReportData) internal.DiscordEmbed {
	color := discordColorHealthy
	hasCritical, hasWatch := false, false
	for _, p := range d.Problems {
		switch p.Tier {
		case score.TierCritical:
			hasCritical = true
		case score.TierWatch:
			hasWatch = true
		}
	}
	switch {
	case hasCritical:
		color = discordColorCritical
	case hasWatch:
		color = discordColorWarning
	}

	var fields []internal.DiscordEmbedField
	if d.AllHealthy {
		fields = append(fields, internal.DiscordEmbedField{
			Name:  "Status",
			Value: fmt.Sprintf("✅ All %d validators healthy", d.TotalCount),
		})
	} else {
		shown, truncatedCount := truncateProblems(d.Problems, maxProblemsDiscord)
		for _, p := range shown {
			vpPct := ""
			if pct, ok := p.VotingPowerPercent(); ok {
				vpPct = fmt.Sprintf(" | VP: %.1f%%", pct)
			}
			fields = append(fields, internal.DiscordEmbedField{
				Name:  p.DisplayName(),
				Value: fmt.Sprintf("Tier: %s | Score: %d | Missed: %d%s", p.Tier, p.Score, p.MissedBlocks, vpPct),
			})
		}
		if truncatedCount > 0 {
			fields = append(fields, internal.DiscordEmbedField{
				Name:  "…",
				Value: truncatedSummaryText(truncatedCount),
			})
		}
	}

	description := d.ChainSummary
	if w := reportWindowText(d); w != "" {
		if description != "" {
			description += "\n"
		}
		description += w
	}
	embed := internal.DiscordEmbed{
		Title:       fmt.Sprintf("[%s] Daily Summary — %s", d.ChainID, d.Date),
		Description: description,
		Color:       color,
		Fields:      fields,
	}
	if d.ReportLink != "" {
		embed.Footer = &internal.DiscordEmbedFooter{Text: "Full report: " + d.ReportLink}
	}
	return embed
}

// RenderDailyReportSlackBlocks builds a Slack Block Kit message summarizing
// the daily report: a header, a chain-summary section, one section per
// problem validator (or a single "all healthy" section), and a context block
// with the report link when set.
func RenderDailyReportSlackBlocks(d DailyReportData) []internal.SlackBlock {
	var blocks []internal.SlackBlock

	blocks = append(blocks, internal.SlackBlock{
		Type: "header",
		Text: &internal.SlackText{Type: "plain_text", Text: fmt.Sprintf("[%s] Daily Summary — %s", d.ChainID, d.Date)},
	})
	if d.ChainSummary != "" {
		blocks = append(blocks, internal.SlackBlock{
			Type: "section",
			Text: &internal.SlackText{Type: "mrkdwn", Text: d.ChainSummary},
		})
	}
	if w := reportWindowText(d); w != "" {
		blocks = append(blocks, internal.SlackBlock{
			Type: "section",
			Text: &internal.SlackText{Type: "mrkdwn", Text: w},
		})
	}
	blocks = append(blocks, internal.SlackBlock{Type: "divider"})

	if d.AllHealthy {
		blocks = append(blocks, internal.SlackBlock{
			Type: "section",
			Text: &internal.SlackText{Type: "mrkdwn", Text: fmt.Sprintf("✅ All %d validators healthy", d.TotalCount)},
		})
	} else {
		shown, truncatedCount := truncateProblems(d.Problems, maxProblemsSlack)
		for _, p := range shown {
			blocks = append(blocks, internal.SlackBlock{
				Type: "section",
				Text: &internal.SlackText{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*%s* (`%s`)\nTier: %s | Score: %d | Missed: %d", p.DisplayName(), p.Addr, p.Tier, p.Score, p.MissedBlocks),
				},
			})
		}
		if truncatedCount > 0 {
			blocks = append(blocks, internal.SlackBlock{
				Type:     "context",
				Elements: []internal.SlackText{{Type: "mrkdwn", Text: truncatedSummaryText(truncatedCount)}},
			})
		}
	}

	if d.ReportLink != "" {
		blocks = append(blocks, internal.SlackBlock{
			Type:     "context",
			Elements: []internal.SlackText{{Type: "mrkdwn", Text: "Full report: " + d.ReportLink}},
		})
	}
	return blocks
}

// RenderDailyReportTelegramHTML formats DailyReportData as an HTML message
// (Telegram parse_mode=HTML) plus an optional link-button's text/URL. Callers
// pass buttonURL to SendTelegramMessageWithButton (empty means "no button").
func RenderDailyReportTelegramHTML(d DailyReportData) (text, buttonText, buttonURL string) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>[%s] Daily Summary — %s</b>\n", html.EscapeString(d.ChainID), d.Date))
	if d.ChainSummary != "" {
		sb.WriteString(html.EscapeString(d.ChainSummary) + "\n")
	}
	if w := reportWindowText(d); w != "" {
		sb.WriteString(html.EscapeString(w) + "\n")
	}
	if d.AllHealthy {
		sb.WriteString(fmt.Sprintf("✅ All %d validators healthy\n", d.TotalCount))
	} else {
		shown, truncatedCount := truncateProblems(d.Problems, maxProblemsTelegram)
		sb.WriteString(fmt.Sprintf("⚠️ %d/%d validators need attention:\n", len(d.Problems), d.TotalCount))
		for _, p := range shown {
			sb.WriteString(fmt.Sprintf("  <b>%s</b> (<code>%s</code>) — Tier: %s | Score: %d | Missed: %d\n",
				html.EscapeString(p.DisplayName()), html.EscapeString(p.Addr), p.Tier, p.Score, p.MissedBlocks))
		}
		if truncatedCount > 0 {
			sb.WriteString(html.EscapeString(truncatedSummaryText(truncatedCount)) + "\n")
		}
	}

	if d.ReportLink != "" {
		return sb.String(), "📊 Full report", d.ReportLink
	}
	return sb.String(), "", ""
}
