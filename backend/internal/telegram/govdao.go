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

func BuildTelegramGovdaoHandlers(token string, db *gorm.DB, defaultChainID string, enabledChains []string) map[string]func(int64, string) {

	limit_default := int64(10)
	return map[string]func(int64, string){
		// status on Govdao proposals
		"/status": func(chatID int64, args string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)
			params := parseParams(args)

			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit value : %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatStatusProposal(db, chainID, limitint)
			if err != nil {
				log.Printf("error get status proposal%s", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}

		},
		// last executed proposals
		"/executedproposals": func(chatID int64, args string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)
			params := parseParams(args)

			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit value : %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := FormatGetLastExecute(db, chainID, limitint)
			if err != nil {
				log.Printf("error get last executed proposal: %s", err)
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/executedproposals", err)
			}
			// last proposal posted
		},
		"/lastproposal": func(chatID int64, args string) {
			chainID := getGovdaoActiveChain(chatID, defaultChainID)

			msg, err := FormatGetLastProposal(db, chainID)
			if err != nil {
				log.Printf("error get last proposal: %s", err)
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

		"/help": func(chatID int64, _ string) {

			msg := formatHelpgovdao()
			_ = SendMessageTelegram(token, chatID, msg)
		},

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
		return " <b>No GovDAO proposals available", nil
	}
	var builder strings.Builder
	builder.WriteString("🗳️ <b>Gov Dao Proposal</b>")

	// limit
	if len(status) < limit {
		limit = len(status)
	}

	for i, r := range status {

		if i >= limit {
			break
		}
		var emoji string
		if r.Status == "ACEPTED" {
			emoji = "✅"
		}
		if r.Status == "IN PROGRESS" {
			emoji = "⏳"
		}
		if r.Status == "REJECTED" {
			emoji = "❌"
		}

		builder.WriteString(fmt.Sprintf(

			"🗳️ <b>Proposal Nº %d</b>: %s\n"+
				"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
				"%s  %s \n\n",

			r.Id,
			r.Title,
			r.Url,
			emoji,
			r.Status,
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
		return " <b>No executed proposals found", nil
	}
	// limit
	if len(status) < limit {
		limit = len(status)
	}
	var builder strings.Builder
	builder.WriteString("🗳️ <b>Last Executed Proposals</b>")

	for i, r := range status {
		if i >= limit {
			break
		}

		format := FormatTelegramMsg(r.Id, r.Title, r.Url, r.Tx)
		builder.WriteString(format)
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
	builder.WriteString("🗳️ <b>Most Recent Proposal</b>")

	for _, r := range status {

		format := FormatTelegramMsg(r.Id, r.Title, r.Url, r.Tx)

		builder.WriteString(format)

	}

	return builder.String(), err
}

func SendReportGovdaoTelegram(id int, title, urlgnoweb, urltx, botoken string, chatid int64) error {
	msg := FormatTelegramMsg(id, title, urlgnoweb, urltx)

	err := SendMessageTelegram(botoken, chatid, msg)
	if err != nil {
		log.Printf("error send govdao telegram  %s", err)
	}

	return nil

}
func FormatTelegramMsg(id int, title, proposalURL, txURL string) string {
	esc := html.EscapeString
	voteURL := fmt.Sprintf("https://memba.samourai.app/dao/gno.land~r~gov~dao/proposal/%d", id)

	return fmt.Sprintf(

		"🗳️ <b>New Proposal Nº %d</b>: %s\n"+
			"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
			"🗒️ Tx: <a href=\"%s\">Gnoscan</a>\n"+
			"🖐️ Interact & Vote: <a href=\"%s\">Open proposal on Memba</a>",
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
	b.WriteString("   ⮑ Params: <code>limit</code> (optional, default: 10)\n")
	b.WriteString("   ⮑ Example: <code>/status limit=5</code>\n\n")

	b.WriteString("• <code>/executedproposals</code> — show the last executed proposals\n")
	b.WriteString("   ⮑ Params: <code>limit</code> (optional, default: 10)\n")
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
