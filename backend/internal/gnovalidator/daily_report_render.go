package gnovalidator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"gorm.io/gorm"
)

// DailyReportData is the channel-neutral summary of one chain's daily
// report. Each channel renderer (plain text, Discord embed, Slack Block Kit,
// Telegram HTML) consumes the same DailyReportData built once per report.
type DailyReportData struct {
	ChainID       string
	Date          string
	ChainSummary  string
	ValsetChanges []ValsetChange
	// Problems holds validators in tier Watch or Critical for the last_24h
	// period, sorted worst (lowest score) first. Empty when AllHealthy.
	Problems   []database.ValidatorReportEntry
	TotalCount int
	AllHealthy bool
	ReportLink string
}

// BuildDailyReportData assembles a DailyReportData for chainID's healthy-chain
// daily report. snap is the already-fetched ChainHealthSnapshot (same one
// SendDailyStatsForUser passes to FormatHealthyReport); minBlock/maxBlock
// bound the valset-changes window, exactly as FormatHealthyReport does today.
func BuildDailyReportData(db *gorm.DB, chainID, date string, snap ChainHealthSnapshot, minBlock, maxBlock int64) (DailyReportData, error) {
	entries, err := database.BuildChainValidatorReport(db, chainID, "last_24h", "")
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
		ChainID:       chainID,
		Date:          date,
		ChainSummary:  chainSummary,
		ValsetChanges: recentChanges,
		Problems:      problems,
		TotalCount:    len(entries),
		AllHealthy:    len(problems) == 0,
		ReportLink:    reportLinkURL(db, chainID),
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
	if d.AllHealthy {
		sb.WriteString(fmt.Sprintf("✅ All %d validators healthy\n", d.TotalCount))
	} else {
		sb.WriteString(fmt.Sprintf("⚠️ %d/%d validators need attention:\n", len(d.Problems), d.TotalCount))
		for _, p := range d.Problems {
			sb.WriteString(fmt.Sprintf("  %s (%s) — Tier: %s | Score: %d | Missed: %d\n",
				p.Moniker, p.Addr, p.Tier, p.Score, p.MissedBlocks))
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
		for _, p := range d.Problems {
			vpPct := ""
			if p.SumVotingPower > 0 {
				vpPct = fmt.Sprintf(" | VP: %.1f%%", float64(p.VotingPower)/float64(p.SumVotingPower)*100)
			}
			fields = append(fields, internal.DiscordEmbedField{
				Name:  p.Moniker,
				Value: fmt.Sprintf("Tier: %s | Score: %d | Missed: %d%s", p.Tier, p.Score, p.MissedBlocks, vpPct),
			})
		}
	}

	embed := internal.DiscordEmbed{
		Title:       fmt.Sprintf("[%s] Daily Summary — %s", d.ChainID, d.Date),
		Description: d.ChainSummary,
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
	blocks = append(blocks, internal.SlackBlock{Type: "divider"})

	if d.AllHealthy {
		blocks = append(blocks, internal.SlackBlock{
			Type: "section",
			Text: &internal.SlackText{Type: "mrkdwn", Text: fmt.Sprintf("✅ All %d validators healthy", d.TotalCount)},
		})
	} else {
		for _, p := range d.Problems {
			blocks = append(blocks, internal.SlackBlock{
				Type: "section",
				Text: &internal.SlackText{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*%s* (`%s`)\nTier: %s | Score: %d | Missed: %d", p.Moniker, p.Addr, p.Tier, p.Score, p.MissedBlocks),
				},
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
