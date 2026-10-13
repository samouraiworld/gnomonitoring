package telegram

import (
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// govdaoLimitDefault and govdaoLimitMax bound the "limit" argument of the
// GovDAO list commands, so a huge value cannot build an oversized reply.
const (
	govdaoLimitDefault = 10
	govdaoLimitMax     = 50
)

// parseGovdaoLimit returns the "limit" argument clamped to
// [1, govdaoLimitMax], or govdaoLimitDefault when absent or not a number.
func parseGovdaoLimit(raw string) int {
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return govdaoLimitDefault
	}
	if limit < 1 {
		return 1
	}
	if limit > govdaoLimitMax {
		return govdaoLimitMax
	}
	return limit
}

func BuildTelegramGovdaoHandlers(token string, db *gorm.DB, defaultChainID string, enabledChains []string) map[string]func(int64, string) {
	help := func(chatID int64, _ string) {
		_ = SendMessageTelegram(token, chatID, formatHelpgovdao())
	}

	return map[string]func(int64, string){
		// status on Govdao proposals
		"/status": func(chatID int64, args string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)
			limit := parseGovdaoLimit(parseParams(args)["limit"])

			msg, err := formatStatusProposal(db, chainID, limit)
			if err != nil {
				log.Printf("error get status proposal: %v", err)
				msg = "⚠️ Unable to fetch GovDAO proposals."
			}
			if err := SendMessageTelegramChunked(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}
		},
		// last executed proposals
		"/executedproposals": func(chatID int64, args string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)
			limit := parseGovdaoLimit(parseParams(args)["limit"])

			msg, err := FormatGetLastExecute(db, chainID, limit)
			if err != nil {
				log.Printf("error get last executed proposal: %v", err)
				msg = "⚠️ Unable to fetch executed proposals."
			}
			if err := SendMessageTelegramChunked(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/executedproposals", err)
			}
		},
		// last proposal posted
		"/lastproposal": func(chatID int64, _ string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)

			msg, err := FormatGetLastProposal(db, chainID)
			if err != nil {
				log.Printf("error get last proposal: %v", err)
				msg = "⚠️ Unable to fetch the last proposal."
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/lastproposal", err)
			}
		},

		"/chain": func(chatID int64, _ string) {
			current := getGovdaoActiveChain(chatID, defaultChainID)
			var b strings.Builder
			b.WriteString(fmt.Sprintf("Current chain: <code>%s</code>\n\nAvailable chains:\n", html.EscapeString(current)))
			for _, id := range enabledChains {
				b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
			}
			b.WriteString("\nUse <code>/setchain chain=&lt;id&gt;</code> to switch.")
			_ = SendMessageTelegram(token, chatID, b.String())
		},

		"/setchain": func(chatID int64, args string) {
			params := parseParams(args)
			requested := strings.TrimSpace(params["chain"])
			if requested == "" {
				_ = SendMessageTelegram(token, chatID, "Usage: <code>/setchain chain=&lt;chain_id&gt;</code>")
				return
			}
			if !validateChainID(requested, enabledChains) {
				var b strings.Builder
				b.WriteString(fmt.Sprintf("Unknown chain <code>%s</code>. Available:\n", html.EscapeString(requested)))
				for _, id := range enabledChains {
					b.WriteString(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(id)))
				}
				_ = SendMessageTelegram(token, chatID, b.String())
				return
			}
			setGovdaoActiveChain(chatID, requested)
			if err := database.UpdateGovdaoChatChain(db, chatID, requested); err != nil {
				log.Printf("⚠️ UpdateGovdaoChatChain chat_id=%d: %v", chatID, err)
			}
			_ = SendMessageTelegram(token, chatID, fmt.Sprintf("Chain set to <code>%s</code>.", html.EscapeString(requested)))
		},

		"/help":  help,
		"/start": help,

		"*": func(chatID int64, _ string) {
			if err := SendMessageTelegram(token, chatID,
				"Unknown command ❓ try /help"); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}
		},
	}
}

func formatStatusProposal(db *gorm.DB, chainID string, limit int) (msg string, err error) {

	status, err := database.GetStatusofGovdao(db, chainID)
	if err != nil {
		return "", fmt.Errorf("failed to get status govdao : %v", err)

	}
	if len(status) == 0 {
		return " <b>No GovDAO proposals available</b>", nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "🗳️ [%s] <b>GovDAO Proposals</b>\n\n", html.EscapeString(chainID))

	// limit
	if len(status) < limit {
		limit = len(status)
	}

	for i, r := range status {

		if i >= limit {
			break
		}
		var emoji string
		switch r.Status {
		case "ACCEPTED":
			emoji = "✅"
		case "ACTIVE", "IN PROGRESS":
			emoji = "⏳"
		case "REJECTED":
			emoji = "❌"
		}

		builder.WriteString(fmt.Sprintf(

			"🗳️ <b>Proposal Nº %d</b>: %s\n"+
				"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
				"%s  %s \n\n",

			r.Id,
			html.EscapeString(r.Title),
			html.EscapeString(r.Url),
			emoji,
			html.EscapeString(r.Status),
		))

	}

	return builder.String(), nil
}
func FormatGetLastExecute(db *gorm.DB, chainID string, limit int) (msg string, err error) {

	status, err := database.GetLastExecute(db, chainID)
	if err != nil {
		return "", fmt.Errorf("failed to get last execute proposal : %v", err)

	}
	if len(status) == 0 {
		return " <b>No executed proposals found</b>", nil
	}
	// limit
	if len(status) < limit {
		limit = len(status)
	}
	var builder strings.Builder
	builder.WriteString("🗳️ <b>Last Executed Proposals</b>\n\n")

	for i, r := range status {
		if i >= limit {
			break
		}

		builder.WriteString(formatProposalBody(chainID, "Executed Proposal", r.Id, r.Title, r.Url, r.Tx))
		builder.WriteString("\n\n")

	}

	return builder.String(), err
}
func FormatGetLastProposal(db *gorm.DB, chainID string) (msg string, err error) {

	status, err := database.GetLastPorposal(db, chainID)
	if err != nil {
		return "", fmt.Errorf("failed to get last proposal : %v", err)

	}
	if len(status) == 0 {
		return " <b>No recent proposals found</b>", nil
	}
	var builder strings.Builder
	builder.WriteString("🗳️ <b>Most Recent Proposal</b>\n\n")

	for _, r := range status {
		builder.WriteString(formatProposalBody(chainID, "Proposal", r.Id, r.Title, r.Url, r.Tx))
	}

	return builder.String(), err
}

func SendReportGovdaoTelegram(chainID string, id int, title, urlgnoweb, urltx, botoken string, chatid int64) error {
	msg := FormatTelegramMsg(chainID, id, title, urlgnoweb, urltx)

	err := SendMessageTelegram(botoken, chatid, msg)
	if err != nil {
		log.Printf("error send govdao telegram  %s", err)
	}

	return nil

}

// MembaProposalURL returns the Memba page where users can read and vote on a
// GovDAO proposal. It is the single source for this link across Discord,
// Slack and Telegram notifications.
func MembaProposalURL(id int) string {
	return fmt.Sprintf("https://memba.club/os/dao/govdao/proposals/%d", id)
}

// FormatTelegramMsg renders the new-proposal notice.
func FormatTelegramMsg(chainID string, id int, title, proposalURL, txURL string) string {
	return formatProposalBody(chainID, "New Proposal", id, title, proposalURL, txURL)
}

// formatProposalBody renders one proposal with its links. label names the
// proposal in the heading ("New Proposal", "Executed Proposal", ...); it is a
// fixed string, not user input, so it is not escaped.
func formatProposalBody(chainID, label string, id int, title, proposalURL, txURL string) string {
	esc := html.EscapeString
	voteURL := MembaProposalURL(id)

	return fmt.Sprintf(

		"🗳️ [%s] <b>%s Nº %d</b>: %s\n"+
			"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
			"🗒️ Tx: <a href=\"%s\">Gnoscan</a>\n"+
			"🖐️ Interact & Vote: <a href=\"%s\">Open proposal on Memba</a> \n"+
			" <b> Make sure you're using the appropriate network on Memba </b>",
		esc(chainID),
		label,
		id,
		esc(title),
		esc(proposalURL),
		esc(txURL),
		esc(voteURL),
	)
}
func formatHelpgovdao() string {
	var b strings.Builder
	b.WriteString("🤖 <b>Gnomonitoring – GovDAO Bot Help</b>\n\n")
	b.WriteString("Use the commands below. Arguments are passed as <code>key=value</code> pairs.\n\n")

	b.WriteString("• <code>/status</code> — list recent GovDAO proposals\n")
	b.WriteString("   ⮑ Params: <code>limit</code> (optional, default: 10, max: 50)\n")
	b.WriteString("   ⮑ Example: <code>/status limit=5</code>\n\n")

	b.WriteString("• <code>/executedproposals</code> — show the last executed proposals\n")
	b.WriteString("   ⮑ Params: <code>limit</code> (optional, default: 10, max: 50)\n")
	b.WriteString("   ⮑ Example: <code>/executedproposals limit=5</code>\n\n")

	b.WriteString("• <code>/lastproposal</code> — show the most recent proposal\n\n")

	b.WriteString("• <code>/chain</code> — list available chains and show current active chain\n\n")

	b.WriteString("• <code>/setchain chain=&lt;chain_id&gt;</code> — switch to a different chain for monitoring proposals\n")
	b.WriteString("   ⮑ Example: <code>/setchain chain=gnoland1</code>\n\n")

	b.WriteString("Formatting notes:\n")
	b.WriteString("• Links open to Gno.land and Gnoscan when available\n")
	b.WriteString("• You can interact & vote via the Memba link when provided\n\n")

	return b.String()
}
